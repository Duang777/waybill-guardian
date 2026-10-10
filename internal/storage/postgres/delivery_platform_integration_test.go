//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	deliveryartifact "github.com/Duang777/waybill-guardian/internal/delivery/artifact"
	deliverydomain "github.com/Duang777/waybill-guardian/internal/delivery/domain"
	deliveryservice "github.com/Duang777/waybill-guardian/internal/delivery/service"
	deliverysource "github.com/Duang777/waybill-guardian/internal/delivery/source"
	deliveryvalidate "github.com/Duang777/waybill-guardian/internal/delivery/validate"
	"github.com/google/uuid"
)

func TestDeliveryPlatformCreatesProblemAndPublishesValidatedCandidate(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := deliverydomain.TenantID("tenant-" + uuid.NewString())
	document := deliverySourceDocument(t, tenantID)
	if err := deliverysource.ImportPostgresSnapshot(t.Context(), db.pool, document); err != nil {
		t.Fatal(err)
	}
	sourceProvider, err := deliverysource.NewPostgresProvider(db.pool)
	if err != nil {
		t.Fatal(err)
	}
	sourceCoordinator, err := deliverysource.NewCoordinator(deliverysource.CoordinatorConfig{
		Provider: sourceProvider,
		Limits:   deliverysource.DefaultLimits(),
		Clock: func() time.Time {
			return document.Manifest.IssuedAt.Add(time.Minute)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	artifactStore, err := deliveryartifact.NewFileStore(t.TempDir(), 128<<20)
	if err != nil {
		t.Fatal(err)
	}
	deliveryStore, err := NewDeliveryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	configDigest := deliveryDigest("d")
	now := document.Manifest.IssuedAt.Add(time.Minute)
	application, err := deliveryservice.NewPlatform(deliveryservice.PlatformConfig{
		Store:     deliveryStore,
		Artifacts: artifactStore,
		Sources: deliveryservice.SourceProfiles{
			"postgres-v1": sourceCoordinator,
		},
		Solvers: deliveryservice.SolverProfiles{
			"deterministic-v1": {
				Solver:       emptyDeliverySolver{},
				ConfigDigest: configDigest,
			},
		},
		Validator: deliveryvalidate.New(deliverydomain.ValidatorIdentity{
			Name:    "delivery-validator",
			Version: "1.0.0",
			Build:   "integration",
		}),
		Clock: func() time.Time {
			return now
		},
		NewID: func(prefix string) string {
			return prefix + "-integration"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(application.Close)

	problem, replay, err := application.Commands().CreateProblem(
		t.Context(),
		deliveryservice.CreateProblem{
			TenantID:       tenantID,
			Actor:          deliveryservice.Actor{Subject: "dispatcher-1"},
			IdempotencyKey: "problem-key",
			ProblemID:      "problem-1",
			Version:        1,
			SourceProfile:  "postgres-v1",
			SnapshotRef:    string(document.Manifest.Ref),
			Horizon: deliverydomain.TimeRange{
				Start: document.Manifest.IssuedAt,
				End:   document.Manifest.IssuedAt.Add(8 * time.Hour),
			},
			Commitments: deliverydomain.CommitmentSet{
				Executed:  []deliverydomain.ExecutedTaskCommitment{},
				Frozen:    []deliverydomain.FrozenTaskCommitment{},
				InTransit: []deliverydomain.InTransitCargoCommitment{},
				Soft:      []deliverydomain.SoftTaskCommitment{},
			},
		},
	)
	if err != nil || replay.Replayed {
		t.Fatalf("CreateProblem = %+v, %+v, %v", problem, replay, err)
	}
	replayedProblem, replay, err := application.Commands().CreateProblem(
		t.Context(),
		deliveryservice.CreateProblem{
			TenantID:       tenantID,
			Actor:          deliveryservice.Actor{Subject: "dispatcher-1"},
			IdempotencyKey: "problem-key",
			ProblemID:      "problem-1",
			Version:        1,
			SourceProfile:  "postgres-v1",
			SnapshotRef:    string(document.Manifest.Ref),
			Horizon: deliverydomain.TimeRange{
				Start: document.Manifest.IssuedAt,
				End:   document.Manifest.IssuedAt.Add(8 * time.Hour),
			},
			Commitments: deliverydomain.CommitmentSet{
				Executed:  []deliverydomain.ExecutedTaskCommitment{},
				Frozen:    []deliverydomain.FrozenTaskCommitment{},
				InTransit: []deliverydomain.InTransitCargoCommitment{},
				Soft:      []deliverydomain.SoftTaskCommitment{},
			},
		},
	)
	if err != nil || !replay.Replayed || replayedProblem != problem {
		t.Fatalf("CreateProblem replay = %+v, %+v, %v",
			replayedProblem, replay, err)
	}
	run, replay, err := application.Commands().RequestOptimization(
		t.Context(),
		deliveryservice.RequestOptimization{
			TenantID:       tenantID,
			Actor:          deliveryservice.Actor{Subject: "dispatcher-1"},
			IdempotencyKey: "run-key",
			ProblemID:      problem.ProblemID,
			ProblemVersion: problem.Version,
			SolverProfile:  "deterministic-v1",
		},
	)
	if err != nil || replay.Replayed {
		t.Fatalf("RequestOptimization = %+v, %+v, %v", run, replay, err)
	}
	worker, err := application.RunWorker(deliveryservice.WorkerConfig{
		TenantID:  tenantID,
		WorkerID:  "worker-1",
		BatchSize: 1,
		LeaseTTL:  time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	didWork, err := worker.RunOnce(t.Context())
	if err != nil || !didWork {
		t.Fatalf("RunOnce = %t, %v", didWork, err)
	}
	completed, err := application.Queries().GetRun(t.Context(), tenantID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != deliverydomain.RunSucceeded ||
		completed.ResultRevisionID == "" ||
		completed.ClosedAt == nil {
		t.Fatalf("completed run = %+v", completed)
	}
	revision, err := application.Queries().GetRevision(
		t.Context(),
		tenantID,
		completed.ResultRevisionID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if revision.Status != deliverydomain.RevisionValidated ||
		revision.PlanDigest == "" ||
		revision.ValidationReportDigest == "" {
		t.Fatalf("revision = %+v", revision)
	}
	reader, metadata, err := application.Queries().OpenArtifact(
		t.Context(),
		tenantID,
		revision.PlanArtifactDigest,
	)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Kind != deliveryartifact.KindPlan {
		t.Fatalf("plan artifact metadata = %+v", metadata)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := application.Queries().OpenArtifact(
		t.Context(),
		"other-tenant",
		revision.PlanArtifactDigest,
	); err == nil {
		t.Fatal("cross-tenant artifact lookup succeeded")
	}
}

type emptyDeliverySolver struct{}

func (emptyDeliverySolver) Identity() deliverydomain.SolverIdentity {
	return deliverydomain.SolverIdentity{
		Name:    "empty-deterministic",
		Version: "1.0.0",
		Build:   "integration",
	}
}

func (emptyDeliverySolver) Solve(
	ctx context.Context,
	problem deliverydomain.ProblemSnapshot,
	request deliveryservice.SolveRequest,
	progress deliveryservice.ProgressSink,
) (deliverydomain.Plan, error) {
	if err := progress.Checkpoint(ctx, map[string]any{
		"phase":       "complete",
		"evaluations": int64(1),
	}); err != nil {
		return deliverydomain.Plan{}, err
	}
	return deliverydomain.Plan{
		SchemaVersion: deliverydomain.PlanSchemaVersion,
		PlanID:        request.PlanID,
		RevisionID:    request.RevisionID,
		Duties:        []deliverydomain.VehicleDuty{},
		Unassigned:    []deliverydomain.UnassignedUnit{},
	}, nil
}

var _ deliveryservice.Solver = emptyDeliverySolver{}
