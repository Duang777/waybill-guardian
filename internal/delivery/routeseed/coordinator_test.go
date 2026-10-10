package routeseed

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/artifact"
	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/delivery/solve"
)

var errInjectedCheckpointWrite = errors.New("injected checkpoint write failure")

func TestCoordinatorRecoversSubmissionWithoutCreatingSecondRemoteJob(
	t *testing.T,
) {
	fixture := newRouteSeedServer(t)
	defer fixture.server.Close()
	provider := fixture.provider(t)
	artifacts, err := artifact.NewFileStore(t.TempDir(), 128<<20)
	if err != nil {
		t.Fatal(err)
	}
	repository := newMemoryRepository()
	repository.failNextState(StatePending)
	problem := routeSeedProblem(t)

	first := newTestCoordinator(t, provider, artifacts, repository)
	_, _, err = first.Advance(t.Context(), problem.TenantID, problem)
	if !errors.Is(err, errInjectedCheckpointWrite) {
		t.Fatalf("first Advance error = %v, want injected failure", err)
	}

	restarted := newTestCoordinator(t, provider, artifacts, repository)
	pending, pendingJob, err := restarted.Advance(
		t.Context(),
		problem.TenantID,
		problem,
	)
	if err != nil {
		t.Fatal(err)
	}
	if pending.State != StatePending ||
		pendingJob.Status != solve.RouteSeedPending ||
		fixture.logicalCreations.Load() != 1 ||
		fixture.posts.Load() != 2 {
		t.Fatalf(
			"pending = %+v, job = %+v, creations = %d, posts = %d",
			pending,
			pendingJob,
			fixture.logicalCreations.Load(),
			fixture.posts.Load(),
		)
	}
	assertArtifactMetadata(
		t,
		artifacts,
		problem.TenantID,
		pending.RequestArtifact,
		artifact.KindRouteSeedRequest,
		solve.RouteSeedRequestSchemaVersion,
	)
	assertArtifactMetadata(
		t,
		artifacts,
		problem.TenantID,
		pending.JobArtifact,
		artifact.KindRouteSeedJob,
		solve.RouteSeedJobSchemaVersion,
	)

	completed, completedJob, err := restarted.Advance(
		t.Context(),
		problem.TenantID,
		problem,
	)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != StateCompleted ||
		completedJob.Status != solve.RouteSeedCompleted ||
		fixture.posts.Load() != 2 ||
		fixture.polls.Load() != 1 {
		t.Fatalf(
			"completed = %+v, job = %+v, posts = %d, polls = %d",
			completed,
			completedJob,
			fixture.posts.Load(),
			fixture.polls.Load(),
		)
	}
}

func TestCoordinatorPersistsCancelIntentAcrossResultWriteFailure(t *testing.T) {
	fixture := newRouteSeedServer(t)
	defer fixture.server.Close()
	provider := fixture.provider(t)
	artifacts, err := artifact.NewFileStore(t.TempDir(), 128<<20)
	if err != nil {
		t.Fatal(err)
	}
	repository := newMemoryRepository()
	problem := routeSeedProblem(t)
	coordinator := newTestCoordinator(t, provider, artifacts, repository)

	pending, _, err := coordinator.Advance(t.Context(), problem.TenantID, problem)
	if err != nil {
		t.Fatal(err)
	}
	if pending.State != StatePending {
		t.Fatalf("pending checkpoint = %+v", pending)
	}
	repository.failNextState(StateCanceled)
	_, _, err = coordinator.Cancel(t.Context(), problem.TenantID, problem)
	if !errors.Is(err, errInjectedCheckpointWrite) {
		t.Fatalf("first Cancel error = %v, want injected failure", err)
	}
	persisted, err := repository.Load(
		t.Context(),
		problem.TenantID,
		pending.RequestDigest,
	)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != StateCancelRequested {
		t.Fatalf("persisted cancellation checkpoint = %+v", persisted)
	}

	restarted := newTestCoordinator(t, provider, artifacts, repository)
	canceled, job, err := restarted.Advance(
		t.Context(),
		problem.TenantID,
		problem,
	)
	if err != nil {
		t.Fatal(err)
	}
	if canceled.State != StateCanceled ||
		job.Status != solve.RouteSeedCanceled ||
		fixture.logicalCancellations.Load() != 1 ||
		fixture.deletes.Load() != 2 {
		t.Fatalf(
			"canceled = %+v, job = %+v, logical cancellations = %d, deletes = %d",
			canceled,
			job,
			fixture.logicalCancellations.Load(),
			fixture.deletes.Load(),
		)
	}
}

func TestCoordinatorTurnsProviderBindingFailureIntoManualReview(t *testing.T) {
	problem := routeSeedProblem(t)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/v1/route-seed/capabilities":
			writeCapabilities(t, writer, solve.RouteSeedORTools, "9.15")
		case "/v1/route-seed/jobs":
			var prepared solve.RouteSeedRequest
			if err := json.NewDecoder(request.Body).Decode(&prepared); err != nil {
				t.Error(err)
				return
			}
			response := routeSeedWireResponse{
				SchemaVersion:  "delivery.route-seed-response.v1",
				Protocol:       solve.RouteSeedORTools,
				JobID:          "job-tampered",
				Status:         solve.RouteSeedPending,
				ProblemDigest:  prepared.ProblemDigest,
				RequestDigest:  prepared.RequestDigest,
				BackendVersion: "9.15",
				BackendBuild:   "test-sidecar",
				ResponseDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			}
			writeJSON(t, writer, response)
		default:
			http.Error(writer, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()
	provider := newRouteSeedHTTPProvider(t, server)
	artifacts, err := artifact.NewFileStore(t.TempDir(), 128<<20)
	if err != nil {
		t.Fatal(err)
	}
	repository := newMemoryRepository()
	coordinator := newTestCoordinator(t, provider, artifacts, repository)

	checkpoint, job, err := coordinator.Advance(
		t.Context(),
		problem.TenantID,
		problem,
	)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.State != StateManualReview ||
		checkpoint.FailureCode != string(solve.RouteSeedErrorBinding) ||
		checkpoint.FailureOperation != "submit" ||
		job.SchemaVersion != "" {
		t.Fatalf("checkpoint = %+v, job = %+v", checkpoint, job)
	}
}

func TestCoordinatorRetriesTransientArtifactReadFailure(t *testing.T) {
	fixture := newRouteSeedServer(t)
	fixture.alwaysPending.Store(true)
	defer fixture.server.Close()
	provider := fixture.provider(t)
	artifacts, err := artifact.NewFileStore(t.TempDir(), 128<<20)
	if err != nil {
		t.Fatal(err)
	}
	repository := newMemoryRepository()
	problem := routeSeedProblem(t)
	coordinator := newTestCoordinator(t, provider, artifacts, repository)
	pending, _, err := coordinator.Advance(t.Context(), problem.TenantID, problem)
	if err != nil {
		t.Fatal(err)
	}
	temporary := errors.New("temporary artifact backend outage")
	failingArtifacts := &failOpenStore{
		Store:  artifacts,
		failAt: 2,
		err:    temporary,
	}
	restarted := newTestCoordinator(
		t,
		provider,
		failingArtifacts,
		repository,
	)
	returned, _, err := restarted.Advance(
		t.Context(),
		problem.TenantID,
		problem,
	)
	if !errors.Is(err, temporary) {
		t.Fatalf("Advance error = %v, want transient artifact error", err)
	}
	if returned.State != StatePending {
		t.Fatalf("returned checkpoint = %+v", returned)
	}
	persisted, err := repository.Load(
		t.Context(),
		problem.TenantID,
		pending.RequestDigest,
	)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != StatePending ||
		persisted.Version != pending.Version {
		t.Fatalf("transient read changed checkpoint = %+v", persisted)
	}
}

func TestCoordinatorRejectsMalformedProviderJobBeforePersistence(t *testing.T) {
	problem := routeSeedProblem(t)
	provider := malformedJobProvider{
		identity: domain.SolverIdentity{
			Name: "malformed", Version: "1.0.0", Build: "bad-build",
		},
	}
	artifacts, err := artifact.NewFileStore(t.TempDir(), 128<<20)
	if err != nil {
		t.Fatal(err)
	}
	repository := newMemoryRepository()
	coordinator := newTestCoordinator(t, provider, artifacts, repository)

	_, _, err = coordinator.Advance(t.Context(), problem.TenantID, problem)
	if !errors.Is(err, ErrCheckpointIntegrity) {
		t.Fatalf("Advance error = %v, want ErrCheckpointIntegrity", err)
	}
	request, err := provider.Prepare(t.Context(), problem)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := repository.Load(
		t.Context(),
		problem.TenantID,
		request.RequestDigest,
	)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != StatePrepared ||
		persisted.JobArtifact != "" ||
		persisted.Version != 1 {
		t.Fatalf("malformed job changed checkpoint = %+v", persisted)
	}
}

func TestCoordinatorConcurrentAdvanceConvergesOnOneCheckpointAndRemoteJob(
	t *testing.T,
) {
	fixture := newRouteSeedServer(t)
	fixture.alwaysPending.Store(true)
	defer fixture.server.Close()
	provider := fixture.provider(t)
	artifacts, err := artifact.NewFileStore(t.TempDir(), 128<<20)
	if err != nil {
		t.Fatal(err)
	}
	repository := newMemoryRepository()
	problem := routeSeedProblem(t)

	const workers = 20
	var group sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			coordinator := newTestCoordinator(t, provider, artifacts, repository)
			checkpoint, _, advanceErr := coordinator.Advance(
				context.Background(),
				problem.TenantID,
				problem,
			)
			if advanceErr == nil && checkpoint.State != StatePending {
				advanceErr = errors.New("concurrent checkpoint is not pending")
			}
			errs <- advanceErr
		}()
	}
	group.Wait()
	close(errs)
	for advanceErr := range errs {
		if advanceErr != nil {
			t.Fatal(advanceErr)
		}
	}
	request, err := provider.Prepare(t.Context(), problem)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := repository.Load(
		t.Context(),
		problem.TenantID,
		request.RequestDigest,
	)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.State != StatePending ||
		fixture.logicalCreations.Load() != 1 {
		t.Fatalf(
			"checkpoint = %+v, logical creations = %d, posts = %d",
			checkpoint,
			fixture.logicalCreations.Load(),
			fixture.posts.Load(),
		)
	}
}

type memoryRepository struct {
	mu        sync.Mutex
	values    map[string]Checkpoint
	failState State
}

type failOpenStore struct {
	artifact.Store
	mu     sync.Mutex
	calls  int
	failAt int
	err    error
}

func (store *failOpenStore) Open(
	ctx context.Context,
	tenantID domain.TenantID,
	digest domain.ArtifactDigest,
) (io.ReadCloser, artifact.Metadata, error) {
	store.mu.Lock()
	store.calls++
	shouldFail := store.calls == store.failAt
	store.mu.Unlock()
	if shouldFail {
		return nil, artifact.Metadata{}, store.err
	}
	return store.Store.Open(ctx, tenantID, digest)
}

type malformedJobProvider struct {
	identity domain.SolverIdentity
}

func (provider malformedJobProvider) Identity() domain.SolverIdentity {
	return provider.identity
}

func (malformedJobProvider) Capabilities(
	context.Context,
) (solve.Capabilities, error) {
	return solve.Capabilities{
		SchemaVersion:         solve.SolverCapabilitiesVersion,
		RemoteJobContinuation: true,
	}, nil
}

func (provider malformedJobProvider) Prepare(
	_ context.Context,
	problem domain.ProblemSnapshot,
) (solve.RouteSeedRequest, error) {
	request := solve.RouteSeedRequest{
		SchemaVersion: solve.RouteSeedRequestSchemaVersion,
		Protocol:      solve.RouteSeedORTools,
		Provider:      provider.identity,
		ProblemDigest: problem.ProblemDigest,
		Problem:       problem,
	}
	digest, err := solve.ComputeRouteSeedRequestDigest(request)
	request.RequestDigest = digest
	return request, err
}

func (provider malformedJobProvider) Submit(
	_ context.Context,
	request solve.RouteSeedRequest,
) (solve.RouteSeedJob, error) {
	return solve.RouteSeedJob{
		SchemaVersion:  solve.RouteSeedJobSchemaVersion,
		Provider:       provider.identity,
		RemoteJobID:    "",
		Status:         solve.RouteSeedPending,
		ProblemDigest:  request.ProblemDigest,
		RequestDigest:  request.RequestDigest,
		ResponseDigest: request.RequestDigest,
		BackendVersion: provider.identity.Version,
		BackendBuild:   provider.identity.Build,
		JobDigest:      request.RequestDigest,
	}, nil
}

func (malformedJobProvider) Poll(
	context.Context,
	solve.RouteSeedJob,
) (solve.RouteSeedJob, error) {
	return solve.RouteSeedJob{}, errors.New("unexpected poll")
}

func (malformedJobProvider) Cancel(
	context.Context,
	solve.RouteSeedJob,
) (solve.RouteSeedJob, error) {
	return solve.RouteSeedJob{}, errors.New("unexpected cancel")
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{values: make(map[string]Checkpoint)}
}

func (repository *memoryRepository) failNextState(state State) {
	repository.mu.Lock()
	repository.failState = state
	repository.mu.Unlock()
}

func (repository *memoryRepository) Load(
	_ context.Context,
	tenantID domain.TenantID,
	requestDigest domain.ArtifactDigest,
) (Checkpoint, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	value, exists := repository.values[checkpointKey(tenantID, requestDigest)]
	if !exists {
		return Checkpoint{}, ErrCheckpointNotFound
	}
	return value, nil
}

func (repository *memoryRepository) Create(
	_ context.Context,
	value Checkpoint,
) (Checkpoint, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	key := checkpointKey(value.TenantID, value.RequestDigest)
	if _, exists := repository.values[key]; exists {
		return Checkpoint{}, ErrCheckpointConflict
	}
	repository.values[key] = value
	return value, nil
}

func (repository *memoryRepository) CompareAndSwap(
	_ context.Context,
	expectedVersion uint64,
	value Checkpoint,
) (Checkpoint, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	key := checkpointKey(value.TenantID, value.RequestDigest)
	current, exists := repository.values[key]
	if !exists {
		return Checkpoint{}, ErrCheckpointNotFound
	}
	if current.Version != expectedVersion ||
		value.Version != expectedVersion+1 {
		return Checkpoint{}, ErrCheckpointConflict
	}
	if repository.failState == value.State {
		repository.failState = ""
		return Checkpoint{}, errInjectedCheckpointWrite
	}
	repository.values[key] = value
	return value, nil
}

func checkpointKey(
	tenantID domain.TenantID,
	requestDigest domain.ArtifactDigest,
) string {
	return string(tenantID) + "\x00" + string(requestDigest)
}

type routeSeedServer struct {
	server               *httptest.Server
	posts                atomic.Int32
	polls                atomic.Int32
	deletes              atomic.Int32
	logicalCreations     atomic.Int32
	logicalCancellations atomic.Int32
	alwaysPending        atomic.Bool
	mu                   sync.Mutex
	jobsByRequest        map[string]string
	canceled             map[string]bool
}

func newRouteSeedServer(t testing.TB) *routeSeedServer {
	t.Helper()
	fixture := &routeSeedServer{
		jobsByRequest: make(map[string]string),
		canceled:      make(map[string]bool),
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == "/v1/route-seed/capabilities":
			writeCapabilities(t, writer, solve.RouteSeedORTools, "9.15")
		case request.Method == http.MethodPost &&
			request.URL.Path == "/v1/route-seed/jobs":
			fixture.posts.Add(1)
			var prepared solve.RouteSeedRequest
			if err := json.NewDecoder(request.Body).Decode(&prepared); err != nil {
				t.Error(err)
				return
			}
			key := request.Header.Get("Idempotency-Key")
			fixture.mu.Lock()
			jobID, exists := fixture.jobsByRequest[key]
			if !exists {
				jobID = "job-stable"
				fixture.jobsByRequest[key] = jobID
				fixture.logicalCreations.Add(1)
			}
			fixture.mu.Unlock()
			writeRouteSeedWireResponse(t, writer, routeSeedWireResponse{
				SchemaVersion:  "delivery.route-seed-response.v1",
				Protocol:       solve.RouteSeedORTools,
				JobID:          jobID,
				Status:         solve.RouteSeedPending,
				ProblemDigest:  prepared.ProblemDigest,
				RequestDigest:  prepared.RequestDigest,
				BackendVersion: "9.15",
			})
		case request.Method == http.MethodGet &&
			request.URL.Path == "/v1/route-seed/jobs/job-stable":
			fixture.polls.Add(1)
			status := solve.RouteSeedCompleted
			var seed solve.RouteSeed
			if fixture.alwaysPending.Load() {
				status = solve.RouteSeedPending
			} else {
				seed = emptyRouteSeed()
			}
			writeRouteSeedWireResponse(t, writer, routeSeedWireResponse{
				SchemaVersion: "delivery.route-seed-response.v1",
				Protocol:      solve.RouteSeedORTools,
				JobID:         "job-stable",
				Status:        status,
				ProblemDigest: domain.ArtifactDigest(
					request.Header.Get("Delivery-Problem-Digest"),
				),
				RequestDigest: domain.ArtifactDigest(
					request.Header.Get("Delivery-Request-Digest"),
				),
				BackendVersion: "9.15",
				Seed:           seed,
			})
		case request.Method == http.MethodDelete &&
			request.URL.Path == "/v1/route-seed/jobs/job-stable":
			fixture.deletes.Add(1)
			fixture.mu.Lock()
			if !fixture.canceled["job-stable"] {
				fixture.canceled["job-stable"] = true
				fixture.logicalCancellations.Add(1)
			}
			fixture.mu.Unlock()
			writeRouteSeedWireResponse(t, writer, routeSeedWireResponse{
				SchemaVersion: "delivery.route-seed-response.v1",
				Protocol:      solve.RouteSeedORTools,
				JobID:         "job-stable",
				Status:        solve.RouteSeedCanceled,
				ProblemDigest: domain.ArtifactDigest(
					request.Header.Get("Delivery-Problem-Digest"),
				),
				RequestDigest: domain.ArtifactDigest(
					request.Header.Get("Delivery-Request-Digest"),
				),
				BackendVersion: "9.15",
			})
		default:
			http.Error(writer, "not found", http.StatusNotFound)
		}
	}))
	return fixture
}

func (fixture *routeSeedServer) provider(
	t testing.TB,
) *solve.HTTPRouteSeedProvider {
	t.Helper()
	return newRouteSeedHTTPProvider(t, fixture.server)
}

func newRouteSeedHTTPProvider(
	t testing.TB,
	server *httptest.Server,
) *solve.HTTPRouteSeedProvider {
	t.Helper()
	provider, err := solve.NewHTTPRouteSeedProvider(solve.HTTPRouteSeedConfig{
		Protocol:         solve.RouteSeedORTools,
		Endpoint:         server.URL,
		Token:            "route-token",
		BackendVersion:   "9.15",
		BackendBuild:     "test-sidecar",
		RetryMaxAttempts: 1,
		RetryBackoff:     time.Microsecond,
		Client:           server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

type capabilitiesWireResponse struct {
	SchemaVersion  string                  `json:"schema_version"`
	Protocol       solve.RouteSeedProtocol `json:"protocol"`
	BackendVersion string                  `json:"backend_version"`
	BackendBuild   string                  `json:"backend_build"`
	Capabilities   solve.Capabilities      `json:"capabilities"`
	ResponseDigest domain.ArtifactDigest   `json:"response_digest"`
}

type routeSeedWireResponse struct {
	SchemaVersion  string                   `json:"schema_version"`
	Protocol       solve.RouteSeedProtocol  `json:"protocol"`
	JobID          string                   `json:"job_id"`
	Status         solve.RouteSeedJobStatus `json:"status"`
	ProblemDigest  domain.ArtifactDigest    `json:"problem_digest"`
	RequestDigest  domain.ArtifactDigest    `json:"request_digest"`
	BackendVersion string                   `json:"backend_version"`
	BackendBuild   string                   `json:"backend_build"`
	Seed           solve.RouteSeed          `json:"seed"`
	FailureCode    string                   `json:"failure_code"`
	ResponseDigest domain.ArtifactDigest    `json:"response_digest"`
}

func writeCapabilities(
	t testing.TB,
	writer http.ResponseWriter,
	protocol solve.RouteSeedProtocol,
	version string,
) {
	t.Helper()
	value := capabilitiesWireResponse{
		SchemaVersion:  "delivery.route-seed-capabilities-response.v1",
		Protocol:       protocol,
		BackendVersion: version,
		BackendBuild:   "test-sidecar",
		Capabilities: solve.Capabilities{
			SchemaVersion:         solve.SolverCapabilitiesVersion,
			MultiDepot:            true,
			MultiTrip:             true,
			PickupDelivery:        true,
			SplitByUnit:           true,
			HeterogeneousFleet:    true,
			DriverRegulations:     true,
			ElectricVehicles:      true,
			ChargingCapacity:      true,
			DynamicCommitments:    true,
			DeterministicReplay:   true,
			RemoteJobContinuation: true,
		},
	}
	digest, err := domain.Digest(value)
	if err != nil {
		t.Fatal(err)
	}
	value.ResponseDigest = digest
	writeJSON(t, writer, value)
}

func writeRouteSeedWireResponse(
	t testing.TB,
	writer http.ResponseWriter,
	value routeSeedWireResponse,
) {
	t.Helper()
	if value.BackendBuild == "" {
		value.BackendBuild = "test-sidecar"
	}
	digest, err := domain.Digest(value)
	if err != nil {
		t.Fatal(err)
	}
	value.ResponseDigest = digest
	writeJSON(t, writer, value)
}

func writeJSON(t testing.TB, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Error(err)
	}
}

func emptyRouteSeed() solve.RouteSeed {
	return solve.RouteSeed{
		SchemaVersion: solve.RouteSeedSchemaVersion,
		Provider: domain.SolverIdentity{
			Name: "ortools", Version: "9.15", Build: "test-sidecar",
		},
		Routes:     []solve.SeedRoute{},
		Unassigned: []domain.FulfillmentUnitID{},
	}
}

func routeSeedProblem(t testing.TB) domain.ProblemSnapshot {
	t.Helper()
	base := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	problem := domain.ProblemSnapshot{
		SchemaVersion: domain.ProblemSchemaVersion,
		TenantID:      "tenant-a",
		ProblemID:     "problem-route-seed",
		Version:       1,
		Horizon: domain.TimeRange{
			Start: base,
			End:   base.Add(12 * time.Hour),
		},
		Locations:  []domain.Location{},
		Depots:     []domain.Depot{},
		Requests:   []domain.TransportRequest{},
		Units:      []domain.FulfillmentUnit{},
		Cargo:      []domain.CargoItem{},
		Vehicles:   []domain.Vehicle{},
		Drivers:    []domain.Driver{},
		Chargers:   []domain.ChargingStation{},
		SourceRefs: []domain.SourceRef{},
		CreatedAt:  base.Add(-time.Hour),
	}
	var err error
	problem.PolicyDigest, err = domain.ComputePolicyDigest(problem.Policy)
	if err != nil {
		t.Fatal(err)
	}
	problem.CommitmentDigest, err =
		domain.ComputeCommitmentDigest(problem.Commitments)
	if err != nil {
		t.Fatal(err)
	}
	problem.ProblemDigest, err = domain.ComputeProblemDigest(problem)
	if err != nil {
		t.Fatal(err)
	}
	return problem
}

func newTestCoordinator(
	t testing.TB,
	provider solve.RouteSeedProvider,
	artifacts artifact.Store,
	repository Repository,
) *Coordinator {
	t.Helper()
	coordinator, err := NewCoordinator(provider, artifacts, repository)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func assertArtifactMetadata(
	t testing.TB,
	store artifact.Store,
	tenantID domain.TenantID,
	digest domain.ArtifactDigest,
	kind artifact.Kind,
	schemaVersion string,
) {
	t.Helper()
	reader, metadata, err := store.Open(t.Context(), tenantID, digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if metadata.Kind != kind || metadata.SchemaVersion != schemaVersion {
		t.Fatalf("artifact metadata = %+v", metadata)
	}
}
