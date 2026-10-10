package solve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
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
	routeSeedResponseVersion             = "delivery.route-seed-response.v1"
	routeSeedCapabilitiesResponseVersion = "delivery.route-seed-capabilities-response.v1"
	defaultRouteSeedTimeout              = 20 * time.Second
	defaultRouteSeedMaxBytes             = 8 << 20
	defaultRouteSeedRetryAttempts        = 3
	defaultRouteSeedRetryBackoff         = 100 * time.Millisecond
	maxRouteSeedRetryAttempts            = 8
)

var (
	remoteJobIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	remoteFailurePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

type HTTPRouteSeedConfig struct {
	Protocol         RouteSeedProtocol
	Endpoint         string
	Token            string
	BackendVersion   string
	BackendBuild     string
	RequestTimeout   time.Duration
	MaxResponseBytes int64
	RetryMaxAttempts int
	RetryBackoff     time.Duration
	Client           *http.Client
}

type HTTPRouteSeedProvider struct {
	protocol         RouteSeedProtocol
	endpoint         *url.URL
	token            string
	identity         domain.SolverIdentity
	requestTimeout   time.Duration
	maxResponseBytes int64
	retryMaxAttempts int
	retryBackoff     time.Duration
	client           *http.Client
}

type routeSeedResponse struct {
	SchemaVersion  string                `json:"schema_version"`
	Protocol       RouteSeedProtocol     `json:"protocol"`
	JobID          string                `json:"job_id"`
	Status         RouteSeedJobStatus    `json:"status"`
	ProblemDigest  domain.ArtifactDigest `json:"problem_digest"`
	RequestDigest  domain.ArtifactDigest `json:"request_digest"`
	BackendVersion string                `json:"backend_version"`
	BackendBuild   string                `json:"backend_build"`
	Seed           RouteSeed             `json:"seed"`
	FailureCode    string                `json:"failure_code"`
	ResponseDigest domain.ArtifactDigest `json:"response_digest"`
}

type routeSeedCapabilitiesResponse struct {
	SchemaVersion  string                `json:"schema_version"`
	Protocol       RouteSeedProtocol     `json:"protocol"`
	BackendVersion string                `json:"backend_version"`
	BackendBuild   string                `json:"backend_build"`
	Capabilities   Capabilities          `json:"capabilities"`
	ResponseDigest domain.ArtifactDigest `json:"response_digest"`
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
	if strings.TrimSpace(config.BackendBuild) == "" {
		return nil, fmt.Errorf("route seed backend build is required")
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
	if config.RetryMaxAttempts == 0 {
		config.RetryMaxAttempts = defaultRouteSeedRetryAttempts
	}
	if config.RetryMaxAttempts < 1 ||
		config.RetryMaxAttempts > maxRouteSeedRetryAttempts {
		return nil, fmt.Errorf(
			"route seed retry attempts must be between 1 and %d",
			maxRouteSeedRetryAttempts,
		)
	}
	if config.RetryBackoff == 0 {
		config.RetryBackoff = defaultRouteSeedRetryBackoff
	}
	if config.RetryBackoff < 0 {
		return nil, fmt.Errorf("route seed retry backoff must be positive")
	}
	client := http.DefaultClient
	if config.Client != nil {
		client = config.Client
	}
	clientCopy := *client
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
			Build:   config.BackendBuild,
		},
		requestTimeout:   config.RequestTimeout,
		maxResponseBytes: config.MaxResponseBytes,
		retryMaxAttempts: config.RetryMaxAttempts,
		retryBackoff:     config.RetryBackoff,
		client:           &clientCopy,
	}, nil
}

func (provider *HTTPRouteSeedProvider) Identity() domain.SolverIdentity {
	return provider.identity
}

func (provider *HTTPRouteSeedProvider) Capabilities(
	ctx context.Context,
) (Capabilities, error) {
	endpoint := *provider.endpoint
	endpoint.Path = path.Join(endpoint.Path, "v1/route-seed/capabilities")
	raw, err := provider.doJSON(
		ctx,
		"discover_capabilities",
		http.MethodGet,
		endpoint.String(),
		nil,
		"",
		"",
		http.StatusOK,
	)
	if err != nil {
		return Capabilities{}, err
	}
	var response routeSeedCapabilitiesResponse
	if err := decodeStrictJSON(raw, &response); err != nil {
		return Capabilities{}, newRouteSeedError(
			RouteSeedErrorProtocol,
			"discover_capabilities",
			0,
			false,
			true,
			err,
		)
	}
	expectedDigest, err := computeCapabilitiesResponseDigest(response)
	if err != nil {
		return Capabilities{}, newRouteSeedError(
			RouteSeedErrorProtocol,
			"discover_capabilities",
			0,
			false,
			true,
			err,
		)
	}
	if response.SchemaVersion != routeSeedCapabilitiesResponseVersion ||
		response.Protocol != provider.protocol ||
		response.BackendVersion != provider.identity.Version ||
		response.BackendBuild != provider.identity.Build ||
		response.Capabilities.SchemaVersion != SolverCapabilitiesVersion ||
		response.ResponseDigest != expectedDigest ||
		!capabilitiesSubset(response.Capabilities, provider.localCapabilities()) {
		return Capabilities{}, newRouteSeedError(
			RouteSeedErrorBinding,
			"discover_capabilities",
			0,
			false,
			true,
			errors.New("capability response binding mismatch"),
		)
	}
	return response.Capabilities, nil
}

func (provider *HTTPRouteSeedProvider) Prepare(
	ctx context.Context,
	problem domain.ProblemSnapshot,
) (RouteSeedRequest, error) {
	if err := ctx.Err(); err != nil {
		return RouteSeedRequest{}, contextRouteSeedError("prepare", err)
	}
	if err := provider.checkCapabilities(problem, provider.localCapabilities()); err != nil {
		return RouteSeedRequest{}, err
	}
	computedProblemDigest, err := domain.ComputeProblemDigest(problem)
	if err != nil || computedProblemDigest != problem.ProblemDigest {
		return RouteSeedRequest{}, newRouteSeedError(
			RouteSeedErrorBinding,
			"prepare",
			0,
			false,
			true,
			errors.New("problem digest mismatch"),
		)
	}
	request := RouteSeedRequest{
		SchemaVersion: RouteSeedRequestSchemaVersion,
		Protocol:      provider.protocol,
		Provider:      provider.identity,
		ProblemDigest: problem.ProblemDigest,
		Problem:       problem,
	}
	request.RequestDigest, err = ComputeRouteSeedRequestDigest(request)
	if err != nil {
		return RouteSeedRequest{}, newRouteSeedError(
			RouteSeedErrorProtocol,
			"prepare",
			0,
			false,
			false,
			fmt.Errorf("digest request: %w", err),
		)
	}
	return request, nil
}

func (provider *HTTPRouteSeedProvider) Submit(
	ctx context.Context,
	request RouteSeedRequest,
) (RouteSeedJob, error) {
	if err := provider.validateRequest(request); err != nil {
		return RouteSeedJob{}, err
	}
	capabilities, err := provider.Capabilities(ctx)
	if err != nil {
		return RouteSeedJob{}, err
	}
	if err := provider.checkCapabilities(request.Problem, capabilities); err != nil {
		return RouteSeedJob{}, err
	}
	body, err := domain.CanonicalJSON(request)
	if err != nil {
		return RouteSeedJob{}, newRouteSeedError(
			RouteSeedErrorProtocol,
			"submit",
			0,
			false,
			false,
			fmt.Errorf("encode request: %w", err),
		)
	}
	endpoint := *provider.endpoint
	endpoint.Path = path.Join(endpoint.Path, "v1/route-seed/jobs")
	raw, err := provider.doJSON(
		ctx,
		"submit",
		http.MethodPost,
		endpoint.String(),
		body,
		request.ProblemDigest,
		request.RequestDigest,
		http.StatusOK,
		http.StatusAccepted,
	)
	if err != nil {
		return RouteSeedJob{}, err
	}
	response, err := decodeRouteSeedResponse(raw, "submit")
	if err != nil {
		return RouteSeedJob{}, err
	}
	return provider.toJob(
		response,
		request.ProblemDigest,
		request.RequestDigest,
		"submit",
	)
}

func (provider *HTTPRouteSeedProvider) Poll(
	ctx context.Context,
	job RouteSeedJob,
) (RouteSeedJob, error) {
	if err := provider.validateJob(job); err != nil {
		return RouteSeedJob{}, err
	}
	if job.Status != RouteSeedPending {
		return job, nil
	}
	endpoint := provider.jobEndpoint(job.RemoteJobID)
	raw, err := provider.doJSON(
		ctx,
		"poll",
		http.MethodGet,
		endpoint,
		nil,
		job.ProblemDigest,
		job.RequestDigest,
		http.StatusOK,
		http.StatusAccepted,
	)
	if err != nil {
		return RouteSeedJob{}, err
	}
	response, err := decodeRouteSeedResponse(raw, "poll")
	if err != nil {
		return RouteSeedJob{}, err
	}
	return provider.toJob(
		response,
		job.ProblemDigest,
		job.RequestDigest,
		"poll",
	)
}

func (provider *HTTPRouteSeedProvider) Cancel(
	ctx context.Context,
	job RouteSeedJob,
) (RouteSeedJob, error) {
	if err := provider.validateJob(job); err != nil {
		return RouteSeedJob{}, err
	}
	if job.Status != RouteSeedPending {
		return job, nil
	}
	raw, err := provider.doJSON(
		ctx,
		"cancel",
		http.MethodDelete,
		provider.jobEndpoint(job.RemoteJobID),
		nil,
		job.ProblemDigest,
		job.RequestDigest,
		http.StatusOK,
		http.StatusAccepted,
	)
	if err != nil {
		return RouteSeedJob{}, err
	}
	response, err := decodeRouteSeedResponse(raw, "cancel")
	if err != nil {
		return RouteSeedJob{}, err
	}
	canceled, err := provider.toJob(
		response,
		job.ProblemDigest,
		job.RequestDigest,
		"cancel",
	)
	if err != nil {
		return RouteSeedJob{}, err
	}
	if canceled.Status != RouteSeedCanceled {
		return RouteSeedJob{}, newRouteSeedError(
			RouteSeedErrorProtocol,
			"cancel",
			0,
			false,
			true,
			fmt.Errorf("provider returned status %q after cancellation", canceled.Status),
		)
	}
	return canceled, nil
}

func (provider *HTTPRouteSeedProvider) jobEndpoint(jobID string) string {
	endpoint := *provider.endpoint
	endpoint.Path = path.Join(
		endpoint.Path,
		"v1/route-seed/jobs",
		url.PathEscape(jobID),
	)
	return endpoint.String()
}

func (provider *HTTPRouteSeedProvider) doJSON(
	ctx context.Context,
	operation string,
	method string,
	endpoint string,
	body []byte,
	problemDigest domain.ArtifactDigest,
	requestDigest domain.ArtifactDigest,
	acceptedStatuses ...int,
) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= provider.retryMaxAttempts; attempt++ {
		raw, err := provider.doJSONAttempt(
			ctx,
			operation,
			method,
			endpoint,
			body,
			problemDigest,
			requestDigest,
			acceptedStatuses,
		)
		if err == nil {
			return raw, nil
		}
		lastErr = err
		var classified *RouteSeedError
		if !errors.As(err, &classified) ||
			!classified.Retryable ||
			attempt == provider.retryMaxAttempts {
			return nil, err
		}
		if err := waitRouteSeedRetry(
			ctx,
			provider.retryBackoff,
			attempt,
		); err != nil {
			return nil, contextRouteSeedError(operation, err)
		}
	}
	return nil, lastErr
}

func (provider *HTTPRouteSeedProvider) doJSONAttempt(
	ctx context.Context,
	operation string,
	method string,
	endpoint string,
	body []byte,
	problemDigest domain.ArtifactDigest,
	requestDigest domain.ArtifactDigest,
	acceptedStatuses []int,
) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, contextRouteSeedError(operation, err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, provider.requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx,
		method,
		endpoint,
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, newRouteSeedError(
			RouteSeedErrorProtocol,
			operation,
			0,
			false,
			false,
			fmt.Errorf("create request: %w", err),
		)
	}
	request.Header.Set("Authorization", "Bearer "+provider.token)
	request.Header.Set("Accept", "application/json")
	if problemDigest != "" {
		request.Header.Set("Delivery-Problem-Digest", string(problemDigest))
	}
	if requestDigest != "" {
		request.Header.Set("Delivery-Request-Digest", string(requestDigest))
	}
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", string(requestDigest))
	}
	httpResponse, err := provider.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, contextRouteSeedError(operation, ctx.Err())
		}
		if requestCtx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			return nil, newRouteSeedError(
				RouteSeedErrorDeadline,
				operation,
				0,
				true,
				false,
				context.DeadlineExceeded,
			)
		}
		return nil, newRouteSeedError(
			RouteSeedErrorUnavailable,
			operation,
			0,
			true,
			false,
			err,
		)
	}
	defer httpResponse.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(
		httpResponse.Body,
		provider.maxResponseBytes+1,
	))
	if err != nil {
		return nil, newRouteSeedError(
			RouteSeedErrorUnavailable,
			operation,
			httpResponse.StatusCode,
			true,
			false,
			fmt.Errorf("read response: %w", err),
		)
	}
	if int64(len(raw)) > provider.maxResponseBytes {
		return nil, newRouteSeedError(
			RouteSeedErrorProtocol,
			operation,
			httpResponse.StatusCode,
			false,
			true,
			errors.New("response exceeds size limit"),
		)
	}
	if slices.Contains(acceptedStatuses, httpResponse.StatusCode) {
		mediaType, _, parseErr := mime.ParseMediaType(
			httpResponse.Header.Get("Content-Type"),
		)
		if parseErr != nil || mediaType != "application/json" {
			return nil, newRouteSeedError(
				RouteSeedErrorProtocol,
				operation,
				httpResponse.StatusCode,
				false,
				true,
				errors.New("response content type must be application/json"),
			)
		}
		return raw, nil
	}
	return nil, routeSeedHTTPStatusError(operation, httpResponse.StatusCode)
}

func (provider *HTTPRouteSeedProvider) toJob(
	response routeSeedResponse,
	problemDigest domain.ArtifactDigest,
	requestDigest domain.ArtifactDigest,
	operation string,
) (RouteSeedJob, error) {
	expectedDigest, err := computeRouteSeedResponseDigest(response)
	if err != nil {
		return RouteSeedJob{}, newRouteSeedError(
			RouteSeedErrorProtocol,
			operation,
			0,
			false,
			true,
			err,
		)
	}
	if response.ResponseDigest != expectedDigest {
		return RouteSeedJob{}, newRouteSeedError(
			RouteSeedErrorBinding,
			operation,
			0,
			false,
			true,
			errors.New("response digest mismatch"),
		)
	}
	if response.SchemaVersion != routeSeedResponseVersion ||
		response.Protocol != provider.protocol ||
		!remoteJobIDPattern.MatchString(response.JobID) ||
		response.ProblemDigest != problemDigest ||
		response.RequestDigest != requestDigest ||
		response.BackendVersion != provider.identity.Version ||
		response.BackendBuild != provider.identity.Build {
		return RouteSeedJob{}, newRouteSeedError(
			RouteSeedErrorBinding,
			operation,
			0,
			false,
			true,
			errors.New("response binding mismatch"),
		)
	}
	switch response.Status {
	case RouteSeedPending:
		if response.Seed.RouteSeedDigest != "" || response.FailureCode != "" {
			return RouteSeedJob{}, newRouteSeedError(
				RouteSeedErrorProtocol,
				operation,
				0,
				false,
				true,
				errors.New("pending response has terminal fields"),
			)
		}
	case RouteSeedCompleted:
		seed, normalizeErr := normalizeRouteSeed(response.Seed)
		if normalizeErr != nil {
			return RouteSeedJob{}, newRouteSeedError(
				RouteSeedErrorProtocol,
				operation,
				0,
				false,
				true,
				normalizeErr,
			)
		}
		if seed.Provider != provider.identity {
			return RouteSeedJob{}, newRouteSeedError(
				RouteSeedErrorBinding,
				operation,
				0,
				false,
				true,
				errors.New("seed provider binding mismatch"),
			)
		}
		response.Seed = seed
		if response.FailureCode != "" {
			return RouteSeedJob{}, newRouteSeedError(
				RouteSeedErrorProtocol,
				operation,
				0,
				false,
				true,
				errors.New("completed response has failure code"),
			)
		}
	case RouteSeedFailed:
		if !remoteFailurePattern.MatchString(response.FailureCode) ||
			response.Seed.RouteSeedDigest != "" {
			return RouteSeedJob{}, newRouteSeedError(
				RouteSeedErrorProtocol,
				operation,
				0,
				false,
				true,
				errors.New("failed response is incomplete"),
			)
		}
	case RouteSeedCanceled:
		if response.Seed.RouteSeedDigest != "" || response.FailureCode != "" {
			return RouteSeedJob{}, newRouteSeedError(
				RouteSeedErrorProtocol,
				operation,
				0,
				false,
				true,
				errors.New("canceled response has terminal result fields"),
			)
		}
	default:
		return RouteSeedJob{}, newRouteSeedError(
			RouteSeedErrorProtocol,
			operation,
			0,
			false,
			true,
			fmt.Errorf("unsupported job status %q", response.Status),
		)
	}
	job := RouteSeedJob{
		SchemaVersion:  RouteSeedJobSchemaVersion,
		Provider:       provider.identity,
		RemoteJobID:    response.JobID,
		Status:         response.Status,
		ProblemDigest:  problemDigest,
		RequestDigest:  requestDigest,
		ResponseDigest: response.ResponseDigest,
		BackendVersion: response.BackendVersion,
		BackendBuild:   response.BackendBuild,
		Seed:           response.Seed,
		FailureCode:    response.FailureCode,
	}
	job.JobDigest, err = ComputeRouteSeedJobDigest(job)
	if err != nil {
		return RouteSeedJob{}, newRouteSeedError(
			RouteSeedErrorProtocol,
			operation,
			0,
			false,
			true,
			fmt.Errorf("digest job: %w", err),
		)
	}
	return job, nil
}

func (provider *HTTPRouteSeedProvider) validateRequest(
	request RouteSeedRequest,
) error {
	problemDigest, err := domain.ComputeProblemDigest(request.Problem)
	if err != nil {
		return newRouteSeedError(
			RouteSeedErrorProtocol,
			"submit",
			0,
			false,
			true,
			err,
		)
	}
	requestDigest, digestErr := ComputeRouteSeedRequestDigest(request)
	if digestErr != nil ||
		request.SchemaVersion != RouteSeedRequestSchemaVersion ||
		request.Protocol != provider.protocol ||
		request.Provider != provider.identity ||
		request.ProblemDigest != request.Problem.ProblemDigest ||
		request.ProblemDigest != problemDigest ||
		request.RequestDigest != requestDigest {
		return newRouteSeedError(
			RouteSeedErrorBinding,
			"submit",
			0,
			false,
			true,
			errors.New("prepared request binding mismatch"),
		)
	}
	return nil
}

func (provider *HTTPRouteSeedProvider) validateJob(job RouteSeedJob) error {
	digest, err := ComputeRouteSeedJobDigest(job)
	if err != nil ||
		job.SchemaVersion != RouteSeedJobSchemaVersion ||
		job.Provider != provider.identity ||
		!remoteJobIDPattern.MatchString(job.RemoteJobID) ||
		!domain.ValidArtifactDigest(job.ProblemDigest) ||
		!domain.ValidArtifactDigest(job.RequestDigest) ||
		!domain.ValidArtifactDigest(job.ResponseDigest) ||
		job.BackendVersion != provider.identity.Version ||
		job.BackendBuild != provider.identity.Build ||
		job.JobDigest != digest {
		return newRouteSeedError(
			RouteSeedErrorInvalidPersisted,
			"resume",
			0,
			false,
			true,
			errors.New("persisted job is invalid"),
		)
	}
	switch job.Status {
	case RouteSeedPending, RouteSeedCompleted, RouteSeedFailed, RouteSeedCanceled:
		return nil
	default:
		return newRouteSeedError(
			RouteSeedErrorInvalidPersisted,
			"resume",
			0,
			false,
			true,
			errors.New("persisted job status is invalid"),
		)
	}
}

func (provider *HTTPRouteSeedProvider) checkCapabilities(
	problem domain.ProblemSnapshot,
	capabilities Capabilities,
) error {
	fail := func(message string) error {
		return newRouteSeedError(
			RouteSeedErrorCapability,
			"plan_capabilities",
			0,
			false,
			false,
			errors.New(message),
		)
	}
	if capabilities.SchemaVersion != SolverCapabilitiesVersion {
		return fail("provider capability schema is unsupported")
	}
	if !capabilities.RemoteJobContinuation {
		return fail("provider cannot resume remote jobs")
	}
	if len(problem.Depots) > 1 && !capabilities.MultiDepot {
		return fail("provider cannot represent multiple depots")
	}
	heterogeneous, err := hasHeterogeneousFleet(problem.Vehicles)
	if err != nil {
		return fail("vehicle profiles cannot be compared")
	}
	if heterogeneous && !capabilities.HeterogeneousFleet {
		return fail("provider cannot represent a heterogeneous fleet")
	}
	if len(problem.Commitments.Executed) > 0 ||
		len(problem.Commitments.Frozen) > 0 ||
		len(problem.Commitments.InTransit) > 0 {
		if !capabilities.DynamicCommitments {
			return fail("provider cannot preserve active hard commitments")
		}
	}
	hasElectricVehicle := false
	for _, vehicle := range problem.Vehicles {
		if vehicle.Energy.Kind == domain.EnergyElectric {
			hasElectricVehicle = true
		}
	}
	if hasElectricVehicle && !capabilities.ElectricVehicles {
		return fail("provider cannot represent electric vehicles")
	}
	if hasElectricVehicle &&
		len(problem.Chargers) > 0 &&
		!capabilities.ChargingCapacity {
		return fail("provider cannot represent charging capacity")
	}
	for _, request := range problem.Requests {
		if request.Split.Mode == domain.SplitByUnit && !capabilities.SplitByUnit {
			return fail("provider cannot represent split fulfillment units")
		}
		for _, task := range request.Tasks {
			if len(task.PredecessorIDs) > 0 && !capabilities.PickupDelivery {
				return fail("provider cannot represent pickup-delivery precedence")
			}
		}
	}
	if provider.protocol == RouteSeedVROOM {
		for _, vehicle := range problem.Vehicles {
			if len(vehicle.Availability) != 1 {
				return fail("VROOM route seed requires one vehicle availability interval")
			}
		}
		for _, request := range problem.Requests {
			if request.Split.SameVehicle && request.Split.Mode == domain.SplitByUnit {
				return fail("VROOM route seed cannot preserve split same-vehicle coupling")
			}
			if !vroomTaskGraph(request) {
				return fail("VROOM route seed supports jobs and pickup-delivery pairs only")
			}
		}
	}
	return nil
}

func (provider *HTTPRouteSeedProvider) localCapabilities() Capabilities {
	capabilities := Capabilities{
		SchemaVersion:         SolverCapabilitiesVersion,
		MultiDepot:            true,
		PickupDelivery:        true,
		SplitByUnit:           true,
		HeterogeneousFleet:    true,
		DriverRegulations:     true,
		RemoteJobContinuation: true,
	}
	if provider.protocol == RouteSeedORTools {
		capabilities.MultiTrip = true
		capabilities.ElectricVehicles = true
		capabilities.ChargingCapacity = true
		capabilities.DynamicCommitments = true
		capabilities.DeterministicReplay = true
	}
	return capabilities
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

func hasHeterogeneousFleet(
	vehicles []domain.Vehicle,
) (bool, error) {
	if len(vehicles) < 2 {
		return false, nil
	}
	profileDigest := func(vehicle domain.Vehicle) (domain.ArtifactDigest, error) {
		vehicle.ID = ""
		vehicle.HomeDepotID = ""
		for index := range vehicle.Compartments {
			vehicle.Compartments[index].ID = ""
		}
		for index := range vehicle.Doors {
			vehicle.Doors[index].ID = ""
			vehicle.Doors[index].CompartmentID = ""
		}
		for index := range vehicle.Axles {
			vehicle.Axles[index].ID = ""
		}
		return domain.Digest(vehicle)
	}
	first, err := profileDigest(vehicles[0])
	if err != nil {
		return false, err
	}
	for _, vehicle := range vehicles[1:] {
		current, err := profileDigest(vehicle)
		if err != nil {
			return false, err
		}
		if current != first {
			return true, nil
		}
	}
	return false, nil
}

func capabilitiesSubset(value, maximum Capabilities) bool {
	return (!value.MultiDepot || maximum.MultiDepot) &&
		(!value.MultiTrip || maximum.MultiTrip) &&
		(!value.PickupDelivery || maximum.PickupDelivery) &&
		(!value.SplitByUnit || maximum.SplitByUnit) &&
		(!value.HeterogeneousFleet || maximum.HeterogeneousFleet) &&
		(!value.DriverRegulations || maximum.DriverRegulations) &&
		(!value.ElectricVehicles || maximum.ElectricVehicles) &&
		(!value.ChargingCapacity || maximum.ChargingCapacity) &&
		(!value.ThreeDimensionalLoad || maximum.ThreeDimensionalLoad) &&
		(!value.AxleAndCenterOfMass || maximum.AxleAndCenterOfMass) &&
		(!value.StopAccessibility || maximum.StopAccessibility) &&
		(!value.DynamicCommitments || maximum.DynamicCommitments) &&
		(!value.DeterministicReplay || maximum.DeterministicReplay) &&
		(!value.RemoteJobContinuation || maximum.RemoteJobContinuation)
}

func computeRouteSeedResponseDigest(
	value routeSeedResponse,
) (domain.ArtifactDigest, error) {
	value.ResponseDigest = ""
	return domain.Digest(value)
}

func computeCapabilitiesResponseDigest(
	value routeSeedCapabilitiesResponse,
) (domain.ArtifactDigest, error) {
	value.ResponseDigest = ""
	return domain.Digest(value)
}

func decodeRouteSeedResponse(
	raw []byte,
	operation string,
) (routeSeedResponse, error) {
	var value routeSeedResponse
	if err := decodeStrictJSON(raw, &value); err != nil {
		return routeSeedResponse{}, newRouteSeedError(
			RouteSeedErrorProtocol,
			operation,
			0,
			false,
			true,
			err,
		)
	}
	return value, nil
}

func decodeStrictJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("decode response: trailing JSON value")
		}
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func routeSeedHTTPStatusError(operation string, status int) error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return newRouteSeedError(
			RouteSeedErrorUnauthorized,
			operation,
			status,
			false,
			false,
			ErrRouteSeedRejected,
		)
	case status == http.StatusRequestTimeout ||
		status == http.StatusTooEarly ||
		status == http.StatusTooManyRequests ||
		status >= http.StatusInternalServerError:
		return newRouteSeedError(
			RouteSeedErrorUnavailable,
			operation,
			status,
			true,
			false,
			ErrRouteSeedUnavailable,
		)
	default:
		return newRouteSeedError(
			RouteSeedErrorRejected,
			operation,
			status,
			false,
			false,
			ErrRouteSeedRejected,
		)
	}
}

func contextRouteSeedError(operation string, err error) error {
	if errors.Is(err, context.Canceled) {
		return newRouteSeedError(
			RouteSeedErrorCanceled,
			operation,
			0,
			false,
			false,
			context.Canceled,
		)
	}
	return newRouteSeedError(
		RouteSeedErrorDeadline,
		operation,
		0,
		false,
		false,
		context.DeadlineExceeded,
	)
}

func newRouteSeedError(
	code RouteSeedErrorCode,
	operation string,
	status int,
	retryable bool,
	manualReview bool,
	cause error,
) error {
	return &RouteSeedError{
		Code:         code,
		Operation:    operation,
		HTTPStatus:   status,
		Retryable:    retryable,
		ManualReview: manualReview,
		cause:        cause,
	}
}

func waitRouteSeedRetry(
	ctx context.Context,
	base time.Duration,
	attempt int,
) error {
	delay := base
	for index := 1; index < attempt; index++ {
		if delay > time.Minute/2 {
			delay = time.Minute
			break
		}
		delay *= 2
	}
	if delay == 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
