package outboxhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/Duang777/waybill-guardian/internal/outbox"
)

const (
	defaultRequestTimeout = 10 * time.Second
	responseBodyLimit     = 64 << 10
)

type Config struct {
	URL     string
	Token   string
	Timeout time.Duration
	Client  *http.Client
}

type Publisher struct {
	url    string
	token  string
	client *http.Client
}

var _ outbox.Publisher = (*Publisher)(nil)

func New(config Config) (*Publisher, error) {
	endpoint, err := validateURL(config.URL)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(config.Token)
	if token == "" {
		return nil, &ConfigError{Field: "token", Message: "is required"}
	}
	if strings.IndexFunc(token, unicode.IsSpace) >= 0 {
		return nil, &ConfigError{Field: "token", Message: "must not contain whitespace"}
	}
	if config.Timeout == 0 {
		config.Timeout = defaultRequestTimeout
	}
	if config.Timeout < 0 {
		return nil, &ConfigError{Field: "timeout", Message: "must be positive"}
	}
	client := http.DefaultClient
	if config.Client != nil {
		client = config.Client
	}
	clientCopy := *client
	clientCopy.Timeout = config.Timeout
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Publisher{
		url:    endpoint.String(),
		token:  token,
		client: &clientCopy,
	}, nil
}

func (p *Publisher) Publish(ctx context.Context, event outbox.Event) outbox.PublishResult {
	body, err := marshalStructuredEvent(event)
	if err != nil {
		return outbox.PublishResult{
			Disposition: outbox.PermanentFailed,
			ErrorCode:   "invalid_event",
		}
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		p.url,
		bytes.NewReader(body),
	)
	if err != nil {
		return outbox.PublishResult{
			Disposition: outbox.PermanentFailed,
			ErrorCode:   "invalid_request",
		}
	}
	request.Header.Set("Authorization", "Bearer "+p.token)
	request.Header.Set("Content-Type", "application/cloudevents+json")
	request.Header.Set("Accept", "application/json")

	response, err := p.client.Do(request)
	if err != nil {
		return outbox.PublishResult{
			Disposition: outbox.RetryableFailed,
			ErrorCode:   "network_error",
		}
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, responseBodyLimit))
	return classifyStatus(response.StatusCode)
}

type structuredEvent struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Subject         string          `json:"subject,omitempty"`
	Time            string          `json:"time"`
	DataContentType string          `json:"datacontenttype"`
	DataSchema      string          `json:"dataschema"`
	Data            json.RawMessage `json:"data"`
}

func marshalStructuredEvent(event outbox.Event) ([]byte, error) {
	if strings.TrimSpace(event.ID) == "" ||
		strings.TrimSpace(event.Source) == "" ||
		strings.TrimSpace(event.Type) == "" ||
		event.Time.IsZero() ||
		event.DataContentType != "application/json" ||
		strings.TrimSpace(event.DataSchema) == "" ||
		!json.Valid(event.Data) {
		return nil, &ConfigError{Field: "event", Message: "is incomplete or invalid"}
	}
	return json.Marshal(structuredEvent{
		SpecVersion:     "1.0",
		ID:              event.ID,
		Source:          event.Source,
		Type:            event.Type,
		Subject:         event.Subject,
		Time:            event.Time.UTC().Format(time.RFC3339Nano),
		DataContentType: event.DataContentType,
		DataSchema:      event.DataSchema,
		Data:            event.Data,
	})
}

func classifyStatus(status int) outbox.PublishResult {
	switch {
	case status >= 200 && status < 300:
		return outbox.PublishResult{Disposition: outbox.Published}
	case status == http.StatusRequestTimeout,
		status == http.StatusTooEarly,
		status == http.StatusTooManyRequests,
		status >= 500:
		return outbox.PublishResult{
			Disposition: outbox.RetryableFailed,
			ErrorCode:   "http_retryable_status",
		}
	case status >= 300 && status < 500:
		return outbox.PublishResult{
			Disposition: outbox.PermanentFailed,
			ErrorCode:   "http_permanent_status",
		}
	default:
		return outbox.PublishResult{
			Disposition: outbox.RetryableFailed,
			ErrorCode:   "http_unexpected_status",
		}
	}
}

func validateURL(raw string) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimSpace(raw))
	if err != nil ||
		endpoint == nil ||
		!endpoint.IsAbs() ||
		endpoint.Host == "" ||
		endpoint.User != nil ||
		endpoint.RawQuery != "" ||
		endpoint.Fragment != "" {
		return nil, &ConfigError{
			Field:   "url",
			Message: "must be an absolute HTTP endpoint without credentials, query, or fragment",
		}
	}
	switch endpoint.Scheme {
	case "https":
		return endpoint, nil
	case "http":
		host := endpoint.Hostname()
		address := net.ParseIP(host)
		if address != nil && address.IsLoopback() {
			return endpoint, nil
		}
	}
	return nil, &ConfigError{
		Field:   "url",
		Message: "must use HTTPS unless the host is a loopback IP",
	}
}

type ConfigError struct {
	Field   string
	Message string
}

func (e *ConfigError) Error() string {
	return "outbox publisher " + e.Field + " " + e.Message
}
