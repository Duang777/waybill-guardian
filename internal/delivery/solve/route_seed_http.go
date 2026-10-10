package solve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

const (
	routeSeedRequestVersion  = "delivery.route-seed-request.v1"
	routeSeedResponseVersion = "delivery.route-seed-response.v1"
	defaultRouteSeedTimeout  = 20 * time.Second
	defaultRouteSeedMaxBytes = 8 << 20
)

type RouteSeedProtocol string

const (
	RouteSeedVROOM   RouteSeedProtocol = "vroom"
	RouteSeedORTools RouteSeedProtocol = "ortools"
)

var remoteJobIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type HTTPRouteSeedConfig struct {
	Protocol         RouteSeedProtocol
	Endpoint         string
	Token            string
	BackendVersion   string
	RequestTimeout   time.Duration
	MaxResponseBytes int64
	Client           *http.Client
}

type HTTPRouteSeedProvider struct {
	protocol         RouteSeedProtocol
	endpoint         *url.URL
	token            string
	identity         domain.SolverIdentity
	requestTimeout   time.Duration
	maxResponseBytes int64
	client           *http.Client
}

type routeSeedRequest struct {
	SchemaVersion string                 `json:"schema_version"`
	Protocol      RouteSeedProtocol      `json:"protocol"`
	ProblemDigest domain.ArtifactDigest  `json:"problem_digest"`
	Problem       domain.ProblemSnapshot `json:"problem"`
}

type routeSeedResponse struct {
	SchemaVersion  string                `json:"schema_version"`
	Protocol       RouteSeedProtocol     `json:"protocol"`
	JobID          string                `json:"job_id"`
	Status         RouteSeedJobStatus    `json:"status"`
	ProblemDigest  domain.ArtifactDigest `json:"problem_digest"`
	RequestDigest  domain.ArtifactDigest `json:"request_digest"`
	BackendVersion string                `json:"backend_version"`
	Seed           RouteSeed             `json:"seed"`
	FailureCode    string                `json:"failure_code"`
}

func NewHTTPRouteSeedProvider(
	config HTTPRouteSeedConfig,
) (*HTTPRouteSeedProvider, error) {
	if config.Protocol != RouteSeedVROOM && config.Protocol != RouteSeedORTools {
		return nil, fmt.Errorf("route seed protocol must be vroom or ortools")
	}
	endpoint, err := validateRouteSeedEndpoint(config.Endpoint)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(config.Token)
	if token == "" || strings.IndexFunc(token, unicode.IsSpace) >= 0 {
		return nil, fmt.Errorf("route seed token is required and must not contain whitespace")
	}
	if strings.TrimSpace(config.BackendVersion) == "" {
		return nil, fmt.Errorf("route seed backend version is required")
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = defaultRouteSeedTimeout
	}
	if config.RequestTimeout < 0 {
		return nil, fmt.Errorf("route seed request timeout must be positive")
	}
	if config.MaxResponseBytes == 0 {
		config.MaxResponseBytes = defaultRouteSeedMaxBytes
	}
	if config.MaxResponseBytes < 1 {
		return nil, fmt.Errorf("route seed response limit must be positive")
	}
	client := http.DefaultClient
	if config.Client != nil {
		client = config.Client
	}
	clientCopy := *client
	clientCopy.Timeout = config.RequestTimeout
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &HTTPRouteSeedProvider{
		protocol: config.Protocol,
		endpoint: endpoint,
		token:    token,
		identity: domain.SolverIdentity{
			Name:    string(config.Protocol),
			Version: config.BackendVersion,
			Build:   "http-route-seed-v1",
		},
		requestTimeout:   config.RequestTimeout,
		maxResponseBytes: config.MaxResponseBytes,
		client:           &clientCopy,
	}, nil
}

func (provider *HTTPRouteSeedProvider) Identity() domain.SolverIdentity {
	return provider.identity
}

func (provider *HTTPRouteSeedProvider) Capabilities(context.Context) (Capabilities, error) {
	capabilities := Capabilities{
		SchemaVersion:         "delivery.solver-capabilities.v1",
		MultiDepot:            true,
		PickupDelivery:        true,
		SplitByUnit:           true,
		HeterogeneousFleet:    true,
		RemoteJobContinuation: true,
	}
	switch provider.protocol {
	case RouteSeedVROOM:
		capabilities.DriverRegulations = true
	case RouteSeedORTools:
		capabilities.MultiTrip = true
		capabilities.DriverRegulations = true
		capabilities.ElectricVehicles = true
		capabilities.ChargingCapacity = true
		capabilities.DynamicCommitments = true
		capabilities.DeterministicReplay = true
	}
	return capabilities, nil
}

func (provider *HTTPRouteSeedProvider) Submit(
	ctx context.Context,
	problem domain.ProblemSnapshot,
) (RouteSeedJob, error) {
	if err := provider.checkCapabilities(problem); err != nil {
		return RouteSeedJob{}, err
	}
	requestValue := routeSeedRequest{
		SchemaVersion: routeSeedRequestVersion,
		Protocol:      provider.protocol,
		ProblemDigest: problem.ProblemDigest,
		Problem:       problem,
	}
	requestDigest, err := domain.Digest(requestValue)
	if err != nil {
		return RouteSeedJob{}, fmt.Errorf("digest route seed request: %w", err)
	}
	body, err := domain.CanonicalJSON(requestValue)
	if err != nil {
		return RouteSeedJob{}, fmt.Errorf("encode route seed request: %w", err)
	}
	endpoint := *provider.endpoint
	endpoint.Path = path.Join(endpoint.Path, "v1/route-seed/jobs")
	response, responseDigest, err := provider.do(
		ctx,
		http.MethodPost,
		endpoint.String(),
		body,
		problem.ProblemDigest,
		requestDigest,
	)
	if err != nil {
		return RouteSeedJob{}, err
	}
	return provider.toJob(response, responseDigest, problem.ProblemDigest, requestDigest)
}

func (provider *HTTPRouteSeedProvider) Poll(
	ctx context.Context,
	job RouteSeedJob,
) (RouteSeedJob, error) {
	if job.Provider != provider.identity ||
		!remoteJobIDPattern.MatchString(job.RemoteJobID) ||
		!domain.ValidArtifactDigest(job.ProblemDigest) ||
		!domain.ValidArtifactDigest(job.RequestDigest) {
		return RouteSeedJob{}, fmt.Errorf("%w: persisted route seed job is invalid", ErrInvalidRouteSeed)
	}
	endpoint := *provider.endpoint
	endpoint.Path = path.Join(
		endpoint.Path,
		"v1/route-seed/jobs",
		url.PathEscape(job.RemoteJobID),
	)
	response, responseDigest, err := provider.do(
		ctx,
		http.MethodGet,
		endpoint.String(),
		nil,
		job.ProblemDigest,
		job.RequestDigest,
	)
	if err != nil {
		return RouteSeedJob{}, err
	}
	return provider.toJob(response, responseDigest, job.ProblemDigest, job.RequestDigest)
}

func (provider *HTTPRouteSeedProvider) do(
	ctx context.Context,
	method string,
	endpoint string,
	body []byte,
	problemDigest domain.ArtifactDigest,
	requestDigest domain.ArtifactDigest,
) (routeSeedResponse, domain.ArtifactDigest, error) {
	requestCtx, cancel := context.WithTimeout(ctx, provider.requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx,
		method,
		endpoint,
		bytes.NewReader(body),
	)
	if err != nil {
		return routeSeedResponse{}, "", fmt.Errorf("create route seed request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+provider.token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Delivery-Problem-Digest", string(problemDigest))
	request.Header.Set("Delivery-Request-Digest", string(requestDigest))
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", string(requestDigest))
	}
	httpResponse, err := provider.client.Do(request)
	if err != nil {
		return routeSeedResponse{}, "", fmt.Errorf("route seed transport: %w", err)
	}
	defer httpResponse.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(
		httpResponse.Body,
		provider.maxResponseBytes+1,
	))
	if err != nil {
		return routeSeedResponse{}, "", fmt.Errorf("read route seed response: %w", err)
	}
	if int64(len(raw)) > provider.maxResponseBytes {
		return routeSeedResponse{}, "", fmt.Errorf("route seed response exceeds size limit")
	}
	if httpResponse.StatusCode != http.StatusOK &&
		httpResponse.StatusCode != http.StatusAccepted {
		return routeSeedResponse{}, "", fmt.Errorf(
			"route seed HTTP status %d",
			httpResponse.StatusCode,
		)
	}
	var value routeSeedResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return routeSeedResponse{}, "", fmt.Errorf("decode route seed response: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return routeSeedResponse{}, "", err
	}
	responseDigest, err := domain.Digest(value)
	if err != nil {
		return routeSeedResponse{}, "", fmt.Errorf("digest route seed response: %w", err)
	}
	return value, responseDigest, nil
}

func (provider *HTTPRouteSeedProvider) toJob(
	response routeSeedResponse,
	responseDigest domain.ArtifactDigest,
	problemDigest domain.ArtifactDigest,
	requestDigest domain.ArtifactDigest,
) (RouteSeedJob, error) {
	if response.SchemaVersion != routeSeedResponseVersion ||
		response.Protocol != provider.protocol ||
		!remoteJobIDPattern.MatchString(response.JobID) ||
		response.ProblemDigest != problemDigest ||
		response.RequestDigest != requestDigest ||
		response.BackendVersion == "" {
		return RouteSeedJob{}, fmt.Errorf("%w: response binding mismatch", ErrInvalidRouteSeed)
	}
	switch response.Status {
	case RouteSeedPending:
		if response.Seed.RouteSeedDigest != "" || response.FailureCode != "" {
			return RouteSeedJob{}, fmt.Errorf("%w: pending response has terminal fields", ErrInvalidRouteSeed)
		}
	case RouteSeedCompleted:
		seed, err := normalizeRouteSeed(response.Seed)
		if err != nil {
			return RouteSeedJob{}, err
		}
		response.Seed = seed
		if response.FailureCode != "" {
			return RouteSeedJob{}, fmt.Errorf("%w: completed response has failure code", ErrInvalidRouteSeed)
		}
	case RouteSeedFailed:
		if response.FailureCode == "" || response.Seed.RouteSeedDigest != "" {
			return RouteSeedJob{}, fmt.Errorf("%w: failed response is incomplete", ErrInvalidRouteSeed)
		}
	default:
		return RouteSeedJob{}, fmt.Errorf("%w: unsupported job status", ErrInvalidRouteSeed)
	}
	return RouteSeedJob{
		Provider:       provider.identity,
		RemoteJobID:    response.JobID,
		Status:         response.Status,
		ProblemDigest:  problemDigest,
		RequestDigest:  requestDigest,
		ResponseDigest: responseDigest,
		BackendVersion: response.BackendVersion,
		Seed:           response.Seed,
		FailureCode:    response.FailureCode,
	}, nil
}

func (provider *HTTPRouteSeedProvider) checkCapabilities(
	problem domain.ProblemSnapshot,
) error {
	if provider.protocol == RouteSeedVROOM {
		if len(problem.Commitments.Executed) > 0 ||
			len(problem.Commitments.Frozen) > 0 ||
			len(problem.Commitments.InTransit) > 0 {
			return fmt.Errorf(
				"%w: VROOM route seed cannot preserve active hard commitments",
				ErrCapability,
			)
		}
		for _, vehicle := range problem.Vehicles {
			if len(vehicle.Availability) != 1 {
				return fmt.Errorf(
					"%w: VROOM route seed requires one vehicle availability interval",
					ErrCapability,
				)
			}
		}
		for _, request := range problem.Requests {
			if request.Split.SameVehicle && request.Split.Mode == domain.SplitByUnit {
				return fmt.Errorf(
					"%w: VROOM route seed cannot preserve split same-vehicle coupling",
					ErrCapability,
				)
			}
			if !vroomTaskGraph(request) {
				return fmt.Errorf(
					"%w: VROOM route seed supports jobs and pickup-delivery pairs only",
					ErrCapability,
				)
			}
		}
	}
	return nil
}

func vroomTaskGraph(request domain.TransportRequest) bool {
	tasks := make(map[domain.TaskID]domain.ServiceTask, len(request.Tasks))
	for _, task := range request.Tasks {
		tasks[task.ID] = task
		if len(task.PredecessorIDs) > 1 {
			return false
		}
	}
	for _, task := range request.Tasks {
		if len(task.PredecessorIDs) == 0 {
			continue
		}
		predecessor := tasks[task.PredecessorIDs[0]]
		if predecessor.Kind != domain.TaskPickup ||
			task.Kind != domain.TaskDelivery ||
			!sameUnitSet(predecessor.UnitIDs, task.UnitIDs) {
			return false
		}
	}
	return true
}

func sameUnitSet(left, right []domain.FulfillmentUnitID) bool {
	left = append([]domain.FulfillmentUnitID(nil), left...)
	right = append([]domain.FulfillmentUnitID(nil), right...)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode route seed response: trailing JSON value")
		}
		return fmt.Errorf("decode route seed response: %w", err)
	}
	return nil
}

func validateRouteSeedEndpoint(raw string) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimSpace(raw))
	if err != nil ||
		endpoint == nil ||
		!endpoint.IsAbs() ||
		endpoint.Host == "" ||
		endpoint.User != nil ||
		endpoint.RawQuery != "" ||
		endpoint.Fragment != "" {
		return nil, fmt.Errorf(
			"route seed endpoint must be an absolute URL without credentials, query, or fragment",
		)
	}
	switch endpoint.Scheme {
	case "https":
		return endpoint, nil
	case "http":
		address := net.ParseIP(endpoint.Hostname())
		if address != nil && address.IsLoopback() {
			return endpoint, nil
		}
	}
	return nil, fmt.Errorf("route seed endpoint must use HTTPS unless it is loopback")
}
