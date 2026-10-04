package tmssandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform"
)

const (
	ProfileID       = "tms-reassign-sandbox-v1"
	AdapterID       = "tms-reassign-sandbox-v1"
	ContractVersion = "v1"
	Environment     = "sandbox"

	manifestPath = "/.well-known/waybill-capabilities"
	mutationPath = "/v1/reassignments"
	lookupPrefix = "/v1/effects/"

	providerOperation = "reassignments"
	requiredKeyScope  = "tenant+operation"
	maxResponseBytes  = 1 << 20
)

var (
	ErrInvalidConfig        = errors.New("invalid TMS sandbox configuration")
	ErrInvalidManifest      = errors.New("invalid TMS sandbox capability manifest")
	ErrReplayableMutation   = errors.New("mutation request body must not be replayable")
	ErrUnsupportedBinding   = errors.New("effect binding is not supported by this adapter")
	ErrInvalidEffectRequest = errors.New("invalid TMS sandbox effect request")
)

type Config struct {
	BaseURL               string
	Token                 string
	Account               string
	RequestTimeout        time.Duration
	StartupTimeout        time.Duration
	ReconciliationHorizon time.Duration
	MaxConsistencyWindow  time.Duration
	Transport             http.RoundTripper
	Clock                 func() time.Time
}

type Adapter struct {
	baseURL        string
	token          string
	account        string
	requestTimeout time.Duration
	clock          func() time.Time
	capability     platform.WriteCapability
	mutationClient *http.Client
	queryClient    *http.Client
}

type manifest struct {
	AdapterID       string               `json:"adapter_id"`
	ContractVersion string               `json:"contract_version"`
	Environment     string               `json:"environment"`
	Capabilities    []manifestCapability `json:"capabilities"`
}

type manifestCapability struct {
	Action                   domain.Action `json:"action"`
	Operation                string        `json:"operation"`
	KeyScope                 string        `json:"key_scope"`
	KeyRetentionSeconds      int64         `json:"key_retention_seconds"`
	ConsistencyWindowSeconds int64         `json:"lookup_consistency_window_seconds"`
	SameRequestReplays       bool          `json:"same_request_replays"`
	MismatchedRequestRejects bool          `json:"mismatched_request_rejects"`
	LookupByKey              bool          `json:"lookup_by_key"`
}

type reassignMutation struct {
	Action    domain.Action    `json:"action"`
	WaybillID domain.WaybillID `json:"waybill_id"`
	CarrierID domain.CarrierID `json:"carrier_id"`
}

func New(ctx context.Context, config Config) (*Adapter, error) {
	if config.Clock == nil {
		config.Clock = time.Now
	}
	baseURL, err := validateConfig(config)
	if err != nil {
		return nil, err
	}
	transport := config.Transport
	if transport == nil {
		transport = http.DefaultTransport.(*http.Transport).Clone()
	}
	checkRedirect := func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	adapter := &Adapter{
		baseURL:        baseURL,
		token:          strings.TrimSpace(config.Token),
		account:        strings.TrimSpace(config.Account),
		requestTimeout: config.RequestTimeout,
		clock:          config.Clock,
		mutationClient: &http.Client{
			Transport:     mutationRoundTripper{base: transport},
			CheckRedirect: checkRedirect,
		},
		queryClient: &http.Client{
			Transport:     transport,
			CheckRedirect: checkRedirect,
		},
	}
	startupCtx, cancel := context.WithTimeout(ctx, config.StartupTimeout)
	defer cancel()
	advertised, err := adapter.fetchManifest(startupCtx)
	if err != nil {
		return nil, err
	}
	capability, err := validateManifest(
		advertised,
		config.RequestTimeout,
		config.ReconciliationHorizon,
		config.MaxConsistencyWindow,
	)
	if err != nil {
		return nil, err
	}
	adapter.capability = capability
	return adapter, nil
}

func (a *Adapter) Capability() platform.WriteCapability {
	return a.capability
}

func (a *Adapter) AdvertisedActions() []domain.Action {
	return []domain.Action{domain.ActionReassign}
}

func (a *Adapter) Bind(
	request platform.EffectRequest,
	key domain.IdempotencyKey,
	createdAt time.Time,
) (platform.EffectBinding, error) {
	if key == "" || createdAt.IsZero() || request.ArgumentsHash == "" {
		return platform.EffectBinding{}, ErrInvalidEffectRequest
	}
	payload, _, err := mutationPayload(request)
	if err != nil {
		return platform.EffectBinding{}, err
	}
	return platform.EffectBinding{
		SchemaVersion:           1,
		Action:                  domain.ActionReassign,
		AdapterID:               a.capability.AdapterID,
		ContractVersion:         a.capability.ContractVersion,
		ProviderOperation:       providerOperation,
		ProviderScopeDigest:     scopeDigest(a.capability.AdapterID, a.account, providerOperation),
		ProviderRequestHash:     digest(payload),
		KeyCreatedAt:            createdAt.UTC(),
		KeyExpiresAt:            createdAt.UTC().Add(a.capability.KeyRetention),
		LookupConsistencyWindow: a.capability.LookupConsistencyWindow,
	}, nil
}

func (a *Adapter) SupportsRecovery(binding platform.EffectBinding) bool {
	return binding.SchemaVersion == 1 &&
		binding.Action == domain.ActionReassign &&
		binding.AdapterID == a.capability.AdapterID &&
		binding.ContractVersion == a.capability.ContractVersion &&
		binding.ProviderOperation == providerOperation &&
		binding.ProviderScopeDigest == scopeDigest(
			a.capability.AdapterID,
			a.account,
			providerOperation,
		) &&
		validDigest(binding.ProviderRequestHash) &&
		!binding.KeyCreatedAt.IsZero() &&
		binding.KeyExpiresAt.Equal(binding.KeyCreatedAt.Add(a.capability.KeyRetention)) &&
		binding.LookupConsistencyWindow == a.capability.LookupConsistencyWindow
}

func (a *Adapter) fetchManifest(ctx context.Context) (manifest, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		a.baseURL+manifestPath,
		nil,
	)
	if err != nil {
		return manifest{}, fmt.Errorf("%w: build manifest request", ErrInvalidManifest)
	}
	a.setHeaders(request)
	response, err := a.queryClient.Do(request)
	if err != nil {
		return manifest{}, fmt.Errorf("%w: fetch capability manifest", ErrInvalidManifest)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return manifest{}, fmt.Errorf(
			"%w: manifest returned HTTP %d",
			ErrInvalidManifest,
			response.StatusCode,
		)
	}
	raw, err := readLimited(response.Body)
	if err != nil {
		return manifest{}, fmt.Errorf("%w: read capability manifest", ErrInvalidManifest)
	}
	var value manifest
	if err := decodeStrict(raw, &value); err != nil {
		return manifest{}, fmt.Errorf("%w: decode capability manifest", ErrInvalidManifest)
	}
	return value, nil
}

func validateManifest(
	value manifest,
	requestTimeout time.Duration,
	reconciliationHorizon time.Duration,
	maxConsistencyWindow time.Duration,
) (platform.WriteCapability, error) {
	if value.AdapterID != AdapterID ||
		value.ContractVersion != ContractVersion ||
		value.Environment != Environment {
		return platform.WriteCapability{}, ErrInvalidManifest
	}
	var advertised *manifestCapability
	for index := range value.Capabilities {
		candidate := &value.Capabilities[index]
		if candidate.Action != domain.ActionReassign {
			continue
		}
		if advertised != nil {
			return platform.WriteCapability{}, ErrInvalidManifest
		}
		advertised = candidate
	}
	if advertised == nil ||
		advertised.Operation != providerOperation ||
		advertised.KeyScope != requiredKeyScope ||
		!advertised.SameRequestReplays ||
		!advertised.MismatchedRequestRejects ||
		!advertised.LookupByKey {
		return platform.WriteCapability{}, ErrInvalidManifest
	}
	retention, err := durationFromSeconds(advertised.KeyRetentionSeconds)
	if err != nil {
		return platform.WriteCapability{}, ErrInvalidManifest
	}
	consistencyWindow, err := durationFromSeconds(advertised.ConsistencyWindowSeconds)
	if err != nil || consistencyWindow > maxConsistencyWindow {
		return platform.WriteCapability{}, ErrInvalidManifest
	}
	requiredRetention := reconciliationHorizon + consistencyWindow
	if requiredRetention < reconciliationHorizon ||
		requiredRetention > time.Duration(math.MaxInt64)-requestTimeout {
		return platform.WriteCapability{}, ErrInvalidManifest
	}
	requiredRetention += requestTimeout
	if retention <= requiredRetention {
		return platform.WriteCapability{}, ErrInvalidManifest
	}
	return platform.WriteCapability{
		Action:                   domain.ActionReassign,
		AdapterID:                value.AdapterID,
		ContractVersion:          value.ContractVersion,
		Environment:              value.Environment,
		KeyScope:                 advertised.KeyScope,
		KeyRetention:             retention,
		LookupConsistencyWindow:  consistencyWindow,
		SameRequestReplays:       true,
		MismatchedRequestRejects: true,
		LookupByKey:              true,
	}, nil
}

func validateConfig(config Config) (string, error) {
	baseURL := strings.TrimSpace(config.BaseURL)
	token := strings.TrimSpace(config.Token)
	account := strings.TrimSpace(config.Account)
	if baseURL == "" ||
		token == "" ||
		account == "" ||
		config.RequestTimeout <= 0 ||
		config.StartupTimeout <= 0 ||
		config.ReconciliationHorizon <= 0 ||
		config.MaxConsistencyWindow <= 0 {
		return "", ErrInvalidConfig
	}
	parsed, err := url.ParseRequestURI(baseURL)
	if err != nil ||
		parsed.Host == "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", ErrInvalidConfig
	}
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return "", ErrInvalidConfig
	}
	return strings.TrimSuffix(baseURL, "/"), nil
}

func mutationPayload(
	request platform.EffectRequest,
) ([]byte, reassignMutation, error) {
	if request.Action != domain.ActionReassign || len(request.Arguments) == 0 {
		return nil, reassignMutation{}, ErrInvalidEffectRequest
	}
	var arguments struct {
		WaybillID domain.WaybillID `json:"waybill_id"`
		CarrierID domain.CarrierID `json:"carrier_id"`
	}
	if err := decodeStrict(request.Arguments, &arguments); err != nil ||
		arguments.WaybillID == "" ||
		arguments.CarrierID == "" {
		return nil, reassignMutation{}, ErrInvalidEffectRequest
	}
	mutation := reassignMutation{
		Action:    domain.ActionReassign,
		WaybillID: arguments.WaybillID,
		CarrierID: arguments.CarrierID,
	}
	payload, err := json.Marshal(mutation)
	if err != nil {
		return nil, reassignMutation{}, ErrInvalidEffectRequest
	}
	return payload, mutation, nil
}

func (a *Adapter) setHeaders(request *http.Request) {
	request.Header.Set("Authorization", "Bearer "+a.token)
	request.Header.Set("Waybill-Account", a.account)
	request.Header.Set("Accept", "application/json")
}

func durationFromSeconds(seconds int64) (time.Duration, error) {
	if seconds <= 0 || seconds > int64(math.MaxInt64)/int64(time.Second) {
		return 0, ErrInvalidManifest
	}
	return time.Duration(seconds) * time.Second, nil
}

func scopeDigest(adapterID, account, operation string) string {
	return digest([]byte(adapterID + "\x00" + account + "\x00" + operation))
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func decodeStrict(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func readLimited(reader io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	return raw, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

type mutationRoundTripper struct {
	base http.RoundTripper
}

func (t mutationRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodPost ||
		request.Body == nil ||
		request.GetBody != nil {
		return nil, ErrReplayableMutation
	}
	return t.base.RoundTrip(request)
}

var _ platform.WriteRuntime = (*Adapter)(nil)
