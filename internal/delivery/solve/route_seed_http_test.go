package solve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func TestHTTPRouteSeedProviderResumesExistingRemoteJob(t *testing.T) {
	problem := solverProblem(t)
	var posts atomic.Int32
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.Header.Get("Authorization") != "Bearer route-token" ||
			request.Header.Get("Delivery-Problem-Digest") != string(problem.ProblemDigest) {
			http.Error(writer, "missing binding headers", http.StatusBadRequest)
			return
		}
		requestDigest := request.Header.Get("Delivery-Request-Digest")
		switch request.Method {
		case http.MethodPost:
			posts.Add(1)
			if request.Header.Get("Idempotency-Key") != requestDigest {
				http.Error(writer, "missing idempotency key", http.StatusBadRequest)
				return
			}
			var envelope routeSeedRequest
			if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
				http.Error(writer, err.Error(), http.StatusBadRequest)
				return
			}
			writer.WriteHeader(http.StatusAccepted)
			writeRouteSeedResponse(t, writer, routeSeedResponse{
				SchemaVersion:  routeSeedResponseVersion,
				Protocol:       RouteSeedVROOM,
				JobID:          "job-123",
				Status:         RouteSeedPending,
				ProblemDigest:  problem.ProblemDigest,
				RequestDigest:  domain.ArtifactDigest(requestDigest),
				BackendVersion: "vroom-1.15.0",
			})
		case http.MethodGet:
			polls.Add(1)
			writeRouteSeedResponse(t, writer, routeSeedResponse{
				SchemaVersion:  routeSeedResponseVersion,
				Protocol:       RouteSeedVROOM,
				JobID:          "job-123",
				Status:         RouteSeedCompleted,
				ProblemDigest:  problem.ProblemDigest,
				RequestDigest:  domain.ArtifactDigest(requestDigest),
				BackendVersion: "vroom-1.15.0",
				Seed: RouteSeed{
					SchemaVersion: RouteSeedSchemaVersion,
					Provider: domain.SolverIdentity{
						Name: "vroom", Version: "1.15.0", Build: "route-master",
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
				},
			})
		default:
			http.Error(writer, "unsupported method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	provider, err := NewHTTPRouteSeedProvider(HTTPRouteSeedConfig{
		Protocol:       RouteSeedVROOM,
		Endpoint:       server.URL,
		Token:          "route-token",
		BackendVersion: "1.15.0",
		Client:         server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := provider.Submit(context.Background(), problem)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != RouteSeedPending || pending.RemoteJobID != "job-123" {
		t.Fatalf("pending job = %+v", pending)
	}
	completed, err := provider.Poll(context.Background(), pending)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != RouteSeedCompleted ||
		!domain.ValidArtifactDigest(completed.Seed.RouteSeedDigest) ||
		!domain.ValidArtifactDigest(completed.ResponseDigest) {
		t.Fatalf("completed job = %+v", completed)
	}
	if posts.Load() != 1 || polls.Load() != 1 {
		t.Fatalf("HTTP calls = %d POST / %d GET, want 1 / 1", posts.Load(), polls.Load())
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
	provider, err := NewHTTPRouteSeedProvider(HTTPRouteSeedConfig{
		Protocol:       RouteSeedVROOM,
		Endpoint:       server.URL,
		Token:          "route-token",
		BackendVersion: "1.15.0",
		Client:         server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = provider.Submit(context.Background(), problem)
	if !errors.Is(err, ErrCapability) {
		t.Fatalf("Submit error = %v, want ErrCapability", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("capability failure made %d network calls", calls.Load())
	}
}

func TestHTTPRouteSeedProviderRejectsUnboundResponse(t *testing.T) {
	problem := solverProblem(t)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		writeRouteSeedResponse(t, writer, routeSeedResponse{
			SchemaVersion:  routeSeedResponseVersion,
			Protocol:       RouteSeedORTools,
			JobID:          "job-wrong",
			Status:         RouteSeedPending,
			ProblemDigest:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			RequestDigest:  domain.ArtifactDigest(request.Header.Get("Delivery-Request-Digest")),
			BackendVersion: "9.15",
		})
	}))
	defer server.Close()
	provider, err := NewHTTPRouteSeedProvider(HTTPRouteSeedConfig{
		Protocol:       RouteSeedORTools,
		Endpoint:       server.URL,
		Token:          "route-token",
		BackendVersion: "9.15",
		Client:         server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = provider.Submit(context.Background(), problem)
	if !errors.Is(err, ErrInvalidRouteSeed) {
		t.Fatalf("Submit error = %v, want ErrInvalidRouteSeed", err)
	}
}

func writeRouteSeedResponse(
	t testing.TB,
	writer http.ResponseWriter,
	value routeSeedResponse,
) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Error(err)
	}
}
