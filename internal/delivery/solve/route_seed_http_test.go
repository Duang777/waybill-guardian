package solve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/delivery/validate"
)

func TestHTTPRouteSeedProviderPersistsIdentityAndResumesRemoteJob(t *testing.T) {
	problem := solverProblem(t)
	var capabilityCalls atomic.Int32
	var posts atomic.Int32
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == "/v1/route-seed/capabilities":
			capabilityCalls.Add(1)
			writeCapabilitiesResponse(
				t,
				writer,
				RouteSeedVROOM,
				"1.15.0",
				testRouteSeedCapabilities(RouteSeedVROOM),
			)
		case request.Method == http.MethodPost &&
			request.URL.Path == "/v1/route-seed/jobs":
			posts.Add(1)
			assertRouteSeedBindingHeaders(t, request, problem.ProblemDigest)
			var envelope RouteSeedRequest
			decodeRequestJSON(t, request, &envelope)
			if request.Header.Get("Idempotency-Key") !=
				string(envelope.RequestDigest) {
				http.Error(writer, "missing idempotency key", http.StatusBadRequest)
				return
			}
			writeRouteSeedResponse(t, writer, routeSeedResponse{
				SchemaVersion:  routeSeedResponseVersion,
				Protocol:       RouteSeedVROOM,
				JobID:          "job-123",
				Status:         RouteSeedPending,
				ProblemDigest:  problem.ProblemDigest,
				RequestDigest:  envelope.RequestDigest,
				BackendVersion: "1.15.0",
			})
		case request.Method == http.MethodGet &&
			request.URL.Path == "/v1/route-seed/jobs/job-123":
			polls.Add(1)
			assertRouteSeedBindingHeaders(t, request, problem.ProblemDigest)
			writeRouteSeedResponse(t, writer, routeSeedResponse{
				SchemaVersion: routeSeedResponseVersion,
				Protocol:      RouteSeedVROOM,
				JobID:         "job-123",
				Status:        RouteSeedCompleted,
				ProblemDigest: problem.ProblemDigest,
				RequestDigest: domain.ArtifactDigest(
					request.Header.Get("Delivery-Request-Digest"),
				),
				BackendVersion: "1.15.0",
				Seed:           testRouteSeed(RouteSeedVROOM, "1.15.0"),
			})
		default:
			http.Error(writer, "unsupported request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	provider := newTestHTTPRouteSeedProvider(
		t,
		server,
		RouteSeedVROOM,
		"1.15.0",
	)
	prepared, err := provider.Prepare(t.Context(), problem)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.SchemaVersion != RouteSeedRequestSchemaVersion ||
		prepared.Provider != provider.Identity() ||
		prepared.ProblemDigest != problem.ProblemDigest ||
		!domain.ValidArtifactDigest(prepared.RequestDigest) {
		t.Fatalf("prepared request = %+v", prepared)
	}
	pending, err := provider.Submit(t.Context(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != RouteSeedPending ||
		pending.RemoteJobID != "job-123" ||
		pending.RequestDigest != prepared.RequestDigest ||
		!domain.ValidArtifactDigest(pending.ResponseDigest) ||
		!domain.ValidArtifactDigest(pending.JobDigest) {
		t.Fatalf("pending job = %+v", pending)
	}

	restarted := newTestHTTPRouteSeedProvider(
		t,
		server,
		RouteSeedVROOM,
		"1.15.0",
	)
	completed, err := restarted.Poll(t.Context(), pending)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != RouteSeedCompleted ||
		completed.RemoteJobID != pending.RemoteJobID ||
		!domain.ValidArtifactDigest(completed.Seed.RouteSeedDigest) ||
		!domain.ValidArtifactDigest(completed.JobDigest) {
		t.Fatalf("completed job = %+v", completed)
	}
	if capabilityCalls.Load() != 1 || posts.Load() != 1 || polls.Load() != 1 {
		t.Fatalf(
			"HTTP calls = %d capability / %d POST / %d poll, want 1 / 1 / 1",
			capabilityCalls.Load(),
			posts.Load(),
			polls.Load(),
		)
	}
}

func TestHTTPRouteSeedProviderRetriesSubmissionWithOneIdempotencyIdentity(
	t *testing.T,
) {
	problem := solverProblem(t)
	var posts atomic.Int32
	var keysMu sync.Mutex
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path == "/v1/route-seed/capabilities" {
			writeCapabilitiesResponse(
				t,
				writer,
				RouteSeedORTools,
				"9.15",
				testRouteSeedCapabilities(RouteSeedORTools),
			)
			return
		}
		if request.Method != http.MethodPost ||
			request.URL.Path != "/v1/route-seed/jobs" {
			http.Error(writer, "not found", http.StatusNotFound)
			return
		}
		current := posts.Add(1)
		keysMu.Lock()
		keys = append(keys, request.Header.Get("Idempotency-Key"))
		keysMu.Unlock()
		if current < 3 {
			http.Error(writer, "retry", http.StatusServiceUnavailable)
			return
		}
		var prepared RouteSeedRequest
		decodeRequestJSON(t, request, &prepared)
		writeRouteSeedResponse(t, writer, routeSeedResponse{
			SchemaVersion:  routeSeedResponseVersion,
			Protocol:       RouteSeedORTools,
			JobID:          "job-retried",
			Status:         RouteSeedPending,
			ProblemDigest:  prepared.ProblemDigest,
			RequestDigest:  prepared.RequestDigest,
			BackendVersion: "9.15",
		})
	}))
	defer server.Close()
	provider := newTestHTTPRouteSeedProvider(
		t,
		server,
		RouteSeedORTools,
		"9.15",
	)
	prepared, err := provider.Prepare(t.Context(), problem)
	if err != nil {
		t.Fatal(err)
	}
	job, err := provider.Submit(t.Context(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	keysMu.Lock()
	gotKeys := append([]string(nil), keys...)
	keysMu.Unlock()
	if job.RemoteJobID != "job-retried" ||
		posts.Load() != 3 ||
		!slices.Equal(gotKeys, []string{
			string(prepared.RequestDigest),
			string(prepared.RequestDigest),
			string(prepared.RequestDigest),
		}) {
		t.Fatalf("job = %+v, posts = %d, keys = %v", job, posts.Load(), gotKeys)
	}
}

func TestHTTPRouteSeedProviderCancellationIsRemoteAndIdempotent(t *testing.T) {
	problem := solverProblem(t)
	var deletes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch {
		case request.URL.Path == "/v1/route-seed/capabilities":
			writeCapabilitiesResponse(
				t,
				writer,
				RouteSeedORTools,
				"9.15",
				testRouteSeedCapabilities(RouteSeedORTools),
			)
		case request.Method == http.MethodPost:
			var prepared RouteSeedRequest
			decodeRequestJSON(t, request, &prepared)
			writeRouteSeedResponse(t, writer, routeSeedResponse{
				SchemaVersion:  routeSeedResponseVersion,
				Protocol:       RouteSeedORTools,
				JobID:          "job-cancel",
				Status:         RouteSeedPending,
				ProblemDigest:  prepared.ProblemDigest,
				RequestDigest:  prepared.RequestDigest,
				BackendVersion: "9.15",
			})
		case request.Method == http.MethodDelete &&
			request.URL.Path == "/v1/route-seed/jobs/job-cancel":
			deletes.Add(1)
			writeRouteSeedResponse(t, writer, routeSeedResponse{
				SchemaVersion: routeSeedResponseVersion,
				Protocol:      RouteSeedORTools,
				JobID:         "job-cancel",
				Status:        RouteSeedCanceled,
				ProblemDigest: problem.ProblemDigest,
				RequestDigest: domain.ArtifactDigest(
					request.Header.Get("Delivery-Request-Digest"),
				),
				BackendVersion: "9.15",
			})
		default:
			http.Error(writer, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()
	provider := newTestHTTPRouteSeedProvider(
		t,
		server,
		RouteSeedORTools,
		"9.15",
	)
	prepared, err := provider.Prepare(t.Context(), problem)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := provider.Submit(t.Context(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := provider.Cancel(t.Context(), pending)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := provider.Cancel(t.Context(), canceled)
	if err != nil {
		t.Fatal(err)
	}
	if canceled.Status != RouteSeedCanceled ||
		replayed.JobDigest != canceled.JobDigest ||
		deletes.Load() != 1 {
		t.Fatalf(
			"canceled = %+v, replayed = %+v, DELETE calls = %d",
			canceled,
			replayed,
			deletes.Load(),
		)
	}
}

func TestVROOMCapabilityGateFailsBeforeNetwork(t *testing.T) {
	problem := solverProblem(t)
	problem.Commitments.Frozen = []domain.FrozenTaskCommitment{{
		TaskID:    "delivery-1",
		VehicleID: "vehicle-1",
		DriverID:  "driver-1",
	}}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		http.ResponseWriter,
		*http.Request,
	) {
		calls.Add(1)
	}))
	defer server.Close()
	provider := newTestHTTPRouteSeedProvider(
		t,
		server,
		RouteSeedVROOM,
		"1.15.0",
	)

	_, err := provider.Prepare(t.Context(), problem)
	if !errors.Is(err, ErrCapability) {
		t.Fatalf("Prepare error = %v, want ErrCapability", err)
	}
	var classified *RouteSeedError
	if !errors.As(err, &classified) ||
		classified.Code != RouteSeedErrorCapability ||
		classified.Retryable ||
		classified.ManualReview {
		t.Fatalf("classified error = %+v", classified)
	}
	if calls.Load() != 0 {
		t.Fatalf("capability failure made %d network calls", calls.Load())
	}
}

func TestRemoteCapabilityShortfallFailsClosed(t *testing.T) {
	problem := solverProblem(t)
	problem.Commitments.Frozen = []domain.FrozenTaskCommitment{{
		TaskID:    "delivery-1",
		VehicleID: "vehicle-1",
		DriverID:  "driver-1",
	}}
	refreshProblemDigests(t, &problem)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		capabilities := testRouteSeedCapabilities(RouteSeedORTools)
		capabilities.DynamicCommitments = false
		writeCapabilitiesResponse(
			t,
			writer,
			RouteSeedORTools,
			"9.15",
			capabilities,
		)
	}))
	defer server.Close()
	provider := newTestHTTPRouteSeedProvider(
		t,
		server,
		RouteSeedORTools,
		"9.15",
	)
	prepared, err := provider.Prepare(t.Context(), problem)
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Submit(t.Context(), prepared)
	if !errors.Is(err, ErrCapability) {
		t.Fatalf("Submit error = %v, want ErrCapability", err)
	}
}

func TestRemoteCapabilityWithoutContinuationFailsClosed(t *testing.T) {
	problem := solverProblem(t)
	var jobCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/v1/route-seed/capabilities" {
			jobCalls.Add(1)
			http.Error(writer, "unexpected job call", http.StatusInternalServerError)
			return
		}
		capabilities := testRouteSeedCapabilities(RouteSeedORTools)
		capabilities.RemoteJobContinuation = false
		writeCapabilitiesResponse(
			t,
			writer,
			RouteSeedORTools,
			"9.15",
			capabilities,
		)
	}))
	defer server.Close()
	provider := newTestHTTPRouteSeedProvider(
		t,
		server,
		RouteSeedORTools,
		"9.15",
	)
	prepared, err := provider.Prepare(t.Context(), problem)
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Submit(t.Context(), prepared)
	if !errors.Is(err, ErrCapability) {
		t.Fatalf("Submit error = %v, want ErrCapability", err)
	}
	if jobCalls.Load() != 0 {
		t.Fatalf("capability failure made %d job calls", jobCalls.Load())
	}
}

func TestHTTPRouteSeedProviderRejectsUnboundOrTamperedResponses(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*routeSeedResponse)
	}{
		{
			name: "problem binding",
			mutate: func(value *routeSeedResponse) {
				value.ProblemDigest =
					"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			},
		},
		{
			name: "response digest",
			mutate: func(value *routeSeedResponse) {
				value.ResponseDigest =
					"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			problem := solverProblem(t)
			server := httptest.NewServer(http.HandlerFunc(func(
				writer http.ResponseWriter,
				request *http.Request,
			) {
				if request.URL.Path == "/v1/route-seed/capabilities" {
					writeCapabilitiesResponse(
						t,
						writer,
						RouteSeedORTools,
						"9.15",
						testRouteSeedCapabilities(RouteSeedORTools),
					)
					return
				}
				var prepared RouteSeedRequest
				decodeRequestJSON(t, request, &prepared)
				response := routeSeedResponse{
					SchemaVersion:  routeSeedResponseVersion,
					Protocol:       RouteSeedORTools,
					JobID:          "job-wrong",
					Status:         RouteSeedPending,
					ProblemDigest:  problem.ProblemDigest,
					RequestDigest:  prepared.RequestDigest,
					BackendVersion: "9.15",
					BackendBuild:   "test-sidecar",
				}
				digest, err := computeRouteSeedResponseDigest(response)
				if err != nil {
					t.Fatal(err)
				}
				response.ResponseDigest = digest
				test.mutate(&response)
				if test.name == "problem binding" {
					response.ResponseDigest, err =
						computeRouteSeedResponseDigest(response)
					if err != nil {
						t.Fatal(err)
					}
				}
				writer.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(writer).Encode(response); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			provider := newTestHTTPRouteSeedProvider(
				t,
				server,
				RouteSeedORTools,
				"9.15",
			)
			prepared, err := provider.Prepare(t.Context(), problem)
			if err != nil {
				t.Fatal(err)
			}
			_, err = provider.Submit(t.Context(), prepared)
			var classified *RouteSeedError
			if !errors.Is(err, ErrRouteSeedIntegrity) ||
				!errors.As(err, &classified) ||
				classified.Code != RouteSeedErrorBinding ||
				!classified.ManualReview ||
				classified.Retryable {
				t.Fatalf("Submit error = %v, classification = %+v", err, classified)
			}
		})
	}
}

func TestHTTPRouteSeedProviderStableTransportErrors(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		wantCode  RouteSeedErrorCode
		retryable bool
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, wantCode: RouteSeedErrorUnauthorized},
		{name: "rejected", status: http.StatusBadRequest, wantCode: RouteSeedErrorRejected},
		{name: "unavailable", status: http.StatusServiceUnavailable, wantCode: RouteSeedErrorUnavailable, retryable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(
				writer http.ResponseWriter,
				_ *http.Request,
			) {
				http.Error(writer, "classified", test.status)
			}))
			defer server.Close()
			provider := newTestHTTPRouteSeedProviderWithConfig(
				t,
				server,
				RouteSeedORTools,
				"9.15",
				func(config *HTTPRouteSeedConfig) {
					config.RetryMaxAttempts = 1
				},
			)
			_, err := provider.Capabilities(t.Context())
			var classified *RouteSeedError
			if !errors.As(err, &classified) ||
				classified.Code != test.wantCode ||
				classified.HTTPStatus != test.status ||
				classified.Retryable != test.retryable {
				t.Fatalf("Capabilities error = %v, classification = %+v", err, classified)
			}
		})
	}
}

func TestHTTPRouteSeedProviderClassifiesTimeoutAndCallerCancellation(t *testing.T) {
	t.Run("provider timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(
			writer http.ResponseWriter,
			_ *http.Request,
		) {
			time.Sleep(40 * time.Millisecond)
			writeCapabilitiesResponse(
				t,
				writer,
				RouteSeedORTools,
				"9.15",
				testRouteSeedCapabilities(RouteSeedORTools),
			)
		}))
		defer server.Close()
		provider := newTestHTTPRouteSeedProviderWithConfig(
			t,
			server,
			RouteSeedORTools,
			"9.15",
			func(config *HTTPRouteSeedConfig) {
				config.RequestTimeout = 5 * time.Millisecond
				config.RetryMaxAttempts = 1
			},
		)
		_, err := provider.Capabilities(t.Context())
		var classified *RouteSeedError
		if !errors.Is(err, context.DeadlineExceeded) ||
			!errors.As(err, &classified) ||
			classified.Code != RouteSeedErrorDeadline ||
			!classified.Retryable {
			t.Fatalf("Capabilities error = %v, classification = %+v", err, classified)
		}
	})

	t.Run("caller cancellation", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(
			http.ResponseWriter,
			*http.Request,
		) {
		}))
		defer server.Close()
		provider := newTestHTTPRouteSeedProvider(
			t,
			server,
			RouteSeedORTools,
			"9.15",
		)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := provider.Capabilities(ctx)
		var classified *RouteSeedError
		if !errors.Is(err, context.Canceled) ||
			!errors.As(err, &classified) ||
			classified.Code != RouteSeedErrorCanceled ||
			classified.Retryable {
			t.Fatalf("Capabilities error = %v, classification = %+v", err, classified)
		}
	})
}

func TestHTTPRouteSeedProviderRejectsTamperedPersistedJobBeforeNetwork(
	t *testing.T,
) {
	problem := solverProblem(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path == "/v1/route-seed/capabilities" {
			writeCapabilitiesResponse(
				t,
				writer,
				RouteSeedORTools,
				"9.15",
				testRouteSeedCapabilities(RouteSeedORTools),
			)
			return
		}
		calls.Add(1)
		var prepared RouteSeedRequest
		decodeRequestJSON(t, request, &prepared)
		writeRouteSeedResponse(t, writer, routeSeedResponse{
			SchemaVersion:  routeSeedResponseVersion,
			Protocol:       RouteSeedORTools,
			JobID:          "job-persisted",
			Status:         RouteSeedPending,
			ProblemDigest:  prepared.ProblemDigest,
			RequestDigest:  prepared.RequestDigest,
			BackendVersion: "9.15",
		})
	}))
	defer server.Close()
	provider := newTestHTTPRouteSeedProvider(
		t,
		server,
		RouteSeedORTools,
		"9.15",
	)
	prepared, err := provider.Prepare(t.Context(), problem)
	if err != nil {
		t.Fatal(err)
	}
	job, err := provider.Submit(t.Context(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	job.RequestDigest =
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	before := calls.Load()
	_, err = provider.Poll(t.Context(), job)
	var classified *RouteSeedError
	if !errors.Is(err, ErrInvalidRouteSeed) ||
		!errors.As(err, &classified) ||
		classified.Code != RouteSeedErrorInvalidPersisted ||
		!classified.ManualReview {
		t.Fatalf("Poll error = %v, classification = %+v", err, classified)
	}
	if calls.Load() != before {
		t.Fatalf("tampered job made %d unexpected network calls", calls.Load()-before)
	}
}

func TestHTTPRouteSeedProviderRejectsCapabilityOverclaim(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		capabilities := testRouteSeedCapabilities(RouteSeedVROOM)
		capabilities.ElectricVehicles = true
		writeCapabilitiesResponse(
			t,
			writer,
			RouteSeedVROOM,
			"1.15.0",
			capabilities,
		)
	}))
	defer server.Close()
	provider := newTestHTTPRouteSeedProvider(
		t,
		server,
		RouteSeedVROOM,
		"1.15.0",
	)
	_, err := provider.Capabilities(t.Context())
	var classified *RouteSeedError
	if !errors.Is(err, ErrRouteSeedIntegrity) ||
		!errors.As(err, &classified) ||
		!classified.ManualReview {
		t.Fatalf("Capabilities error = %v, classification = %+v", err, classified)
	}
}

func TestBuiltinSolverRemainsAvailableWhenRouteSeedProviderIsUnavailable(
	t *testing.T,
) {
	problem := solverProblem(t)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		http.Error(writer, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	provider := newTestHTTPRouteSeedProviderWithConfig(
		t,
		server,
		RouteSeedORTools,
		"9.15",
		func(config *HTTPRouteSeedConfig) {
			config.RetryMaxAttempts = 1
		},
	)
	prepared, err := provider.Prepare(t.Context(), problem)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Submit(t.Context(), prepared); !errors.Is(
		err,
		ErrRouteSeedUnavailable,
	) {
		t.Fatalf("Submit error = %v, want ErrRouteSeedUnavailable", err)
	}

	builtin, err := NewBuiltin(
		domain.SolverIdentity{
			Name: "builtin", Version: "1.0.0", Build: "fallback-test",
		},
		validate.New(domain.ValidatorIdentity{
			Name: "independent", Version: "1.0.0", Build: "fallback-test",
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := builtin.Solve(
		t.Context(),
		problem,
		solveConfig(problem),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != SolveCompleted || !result.Validation.Valid {
		t.Fatalf(
			"fallback status = %q, violations = %+v",
			result.Status,
			result.Validation.Violations,
		)
	}
}

func TestHTTPRouteSeedProviderRejectsUnsafeEndpointAndRetryConfiguration(
	t *testing.T,
) {
	tests := []HTTPRouteSeedConfig{
		{
			Protocol:       RouteSeedORTools,
			Endpoint:       "http://example.com",
			Token:          "token",
			BackendVersion: "9.15",
			BackendBuild:   "test-sidecar",
		},
		{
			Protocol:         RouteSeedORTools,
			Endpoint:         "https://solver.example.com",
			Token:            "token",
			BackendVersion:   "9.15",
			BackendBuild:     "test-sidecar",
			RetryMaxAttempts: maxRouteSeedRetryAttempts + 1,
		},
	}
	for index, config := range tests {
		if _, err := NewHTTPRouteSeedProvider(config); err == nil {
			t.Fatalf("config %d unexpectedly succeeded", index)
		}
	}
}

func newTestHTTPRouteSeedProvider(
	t testing.TB,
	server *httptest.Server,
	protocol RouteSeedProtocol,
	version string,
) *HTTPRouteSeedProvider {
	t.Helper()
	return newTestHTTPRouteSeedProviderWithConfig(
		t,
		server,
		protocol,
		version,
		func(*HTTPRouteSeedConfig) {},
	)
}

func newTestHTTPRouteSeedProviderWithConfig(
	t testing.TB,
	server *httptest.Server,
	protocol RouteSeedProtocol,
	version string,
	mutate func(*HTTPRouteSeedConfig),
) *HTTPRouteSeedProvider {
	t.Helper()
	config := HTTPRouteSeedConfig{
		Protocol:         protocol,
		Endpoint:         server.URL,
		Token:            "route-token",
		BackendVersion:   version,
		BackendBuild:     "test-sidecar",
		RetryMaxAttempts: 3,
		RetryBackoff:     time.Microsecond,
		Client:           server.Client(),
	}
	mutate(&config)
	provider, err := NewHTTPRouteSeedProvider(config)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func testRouteSeedCapabilities(protocol RouteSeedProtocol) Capabilities {
	capabilities := Capabilities{
		SchemaVersion:         SolverCapabilitiesVersion,
		MultiDepot:            true,
		PickupDelivery:        true,
		SplitByUnit:           true,
		HeterogeneousFleet:    true,
		DriverRegulations:     true,
		RemoteJobContinuation: true,
	}
	if protocol == RouteSeedORTools {
		capabilities.MultiTrip = true
		capabilities.ElectricVehicles = true
		capabilities.ChargingCapacity = true
		capabilities.DynamicCommitments = true
		capabilities.DeterministicReplay = true
	}
	return capabilities
}

func testRouteSeed(protocol RouteSeedProtocol, version string) RouteSeed {
	return RouteSeed{
		SchemaVersion: RouteSeedSchemaVersion,
		Provider: domain.SolverIdentity{
			Name: string(protocol), Version: version, Build: "test-sidecar",
		},
		Routes: []SeedRoute{{
			VehicleID: "vehicle-1",
			DriverID:  "driver-1",
			DepotID:   "depot-1",
			TaskIDs: []domain.TaskID{
				"pickup-1", "pickup-2", "delivery-1", "delivery-2",
			},
		}},
		Unassigned: []domain.FulfillmentUnitID{},
	}
}

func assertRouteSeedBindingHeaders(
	t testing.TB,
	request *http.Request,
	problemDigest domain.ArtifactDigest,
) {
	t.Helper()
	if request.Header.Get("Authorization") != "Bearer route-token" ||
		request.Header.Get("Delivery-Problem-Digest") != string(problemDigest) ||
		!domain.ValidArtifactDigest(domain.ArtifactDigest(
			request.Header.Get("Delivery-Request-Digest"),
		)) {
		t.Errorf("route seed request binding headers are invalid")
	}
}

func decodeRequestJSON(t testing.TB, request *http.Request, target any) {
	t.Helper()
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Error(err)
	}
}

func writeCapabilitiesResponse(
	t testing.TB,
	writer http.ResponseWriter,
	protocol RouteSeedProtocol,
	version string,
	capabilities Capabilities,
) {
	t.Helper()
	value := routeSeedCapabilitiesResponse{
		SchemaVersion:  routeSeedCapabilitiesResponseVersion,
		Protocol:       protocol,
		BackendVersion: version,
		BackendBuild:   "test-sidecar",
		Capabilities:   capabilities,
	}
	digest, err := computeCapabilitiesResponseDigest(value)
	if err != nil {
		t.Fatal(err)
	}
	value.ResponseDigest = digest
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Error(err)
	}
}

func writeRouteSeedResponse(
	t testing.TB,
	writer http.ResponseWriter,
	value routeSeedResponse,
) {
	t.Helper()
	if value.BackendBuild == "" {
		value.BackendBuild = "test-sidecar"
	}
	digest, err := computeRouteSeedResponseDigest(value)
	if err != nil {
		t.Fatal(err)
	}
	value.ResponseDigest = digest
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Error(err)
	}
}

func refreshProblemDigests(
	t testing.TB,
	problem *domain.ProblemSnapshot,
) {
	t.Helper()
	var err error
	problem.CommitmentDigest, err = domain.ComputeCommitmentDigest(problem.Commitments)
	if err != nil {
		t.Fatal(err)
	}
	problem.ProblemDigest, err = domain.ComputeProblemDigest(*problem)
	if err != nil {
		t.Fatal(err)
	}
}
