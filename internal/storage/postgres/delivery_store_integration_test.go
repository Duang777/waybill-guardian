//go:build integration

package postgres

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	deliverydomain "github.com/Duang777/waybill-guardian/internal/delivery/domain"
	deliveryservice "github.com/Duang777/waybill-guardian/internal/delivery/service"
	"github.com/google/uuid"
)

func TestDeliveryStoreConcurrentCommandsLeaseAndEventStream(t *testing.T) {
	db := openIntegrationDB(t)
	store, err := NewDeliveryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	tenantID := deliverydomain.TenantID("tenant-" + uuid.NewString())
	problem := deliverydomain.ProblemVersion{
		TenantID:         tenantID,
		ProblemID:        "problem-1",
		Version:          1,
		ProblemDigest:    deliveryDigest("1"),
		PolicyDigest:     deliveryDigest("2"),
		CommitmentDigest: deliveryDigest("3"),
		ManifestDigest:   deliveryDigest("4"),
		ProblemArtifact:  deliveryDigest("5"),
		SourceProfile:    "postgres-v1",
		SourceRef:        "cut-1",
		CreatedAt:        now,
	}
	const writers = 20
	problemResults := make(chan deliverydomain.ProblemVersion, writers)
	problemReplays := make(chan bool, writers)
	errs := make(chan error, writers)
	var wait sync.WaitGroup
	for range writers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, replay, err := store.CommitProblem(t.Context(), deliveryservice.CommitProblemTx{
				IdempotencyKey: "create-problem-key",
				RequestDigest:  deliveryDigest("a"),
				Actor:          deliveryservice.Actor{Subject: "dispatcher-1"},
				Problem:        problem,
			})
			problemResults <- result
			problemReplays <- replay.Replayed
			errs <- err
		}()
	}
	wait.Wait()
	close(problemResults)
	close(problemReplays)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("CommitProblem: %v", err)
		}
	}
	for result := range problemResults {
		if result != problem {
			t.Fatalf("problem replay = %+v, want %+v", result, problem)
		}
	}
	replayCount := 0
	for replayed := range problemReplays {
		if replayed {
			replayCount++
		}
	}
	if replayCount != writers-1 {
		t.Fatalf("problem replay count = %d, want %d", replayCount, writers-1)
	}

	runResults := make(chan deliverydomain.OptimizationRun, writers)
	runReplays := make(chan bool, writers)
	errs = make(chan error, writers)
	for index := range writers {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			run := deliverydomain.OptimizationRun{
				TenantID:       tenantID,
				ID:             deliverydomain.OptimizationRunID(uuid.NewString()),
				ProblemID:      problem.ProblemID,
				ProblemVersion: problem.Version,
				ProblemDigest:  problem.ProblemDigest,
				SolverProfile:  "solver-v1",
				ConfigDigest:   deliveryDigest("6"),
				Status:         deliverydomain.RunQueued,
				Version:        1,
				CreatedAt:      now.Add(time.Duration(index) * time.Nanosecond),
				UpdatedAt:      now.Add(time.Duration(index) * time.Nanosecond),
			}
			result, replay, err := store.CreateRun(t.Context(), deliveryservice.CreateRunTx{
				IdempotencyKey: "create-run-key",
				RequestDigest:  deliveryDigest("b"),
				Actor:          deliveryservice.Actor{Subject: "dispatcher-1"},
				Run:            run,
			})
			runResults <- result
			runReplays <- replay.Replayed
			errs <- err
		}(index)
	}
	wait.Wait()
	close(runResults)
	close(runReplays)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
	}
	var run deliverydomain.OptimizationRun
	for result := range runResults {
		if run.ID == "" {
			run = result
		} else if result.ID != run.ID {
			t.Fatalf("run IDs differ: %s != %s", result.ID, run.ID)
		}
	}
	replayCount = 0
	for replayed := range runReplays {
		if replayed {
			replayCount++
		}
	}
	if replayCount != writers-1 {
		t.Fatalf("run replay count = %d, want %d", replayCount, writers-1)
	}

	claims, err := store.ClaimRuns(t.Context(), deliveryservice.ClaimRuns{
		TenantID: tenantID,
		WorkerID: "worker-1",
		Limit:    1,
		LeaseTTL: time.Minute,
		Now:      now.Add(time.Minute),
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("first ClaimRuns = %+v, %v", claims, err)
	}
	firstClaim := claims[0]
	claims, err = store.ClaimRuns(t.Context(), deliveryservice.ClaimRuns{
		TenantID: tenantID,
		WorkerID: "worker-2",
		Limit:    1,
		LeaseTTL: time.Minute,
		Now:      now.Add(3 * time.Minute),
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("second ClaimRuns = %+v, %v", claims, err)
	}
	secondClaim := claims[0]
	if secondClaim.FencingToken <= firstClaim.FencingToken {
		t.Fatalf("fencing tokens = %d then %d",
			firstClaim.FencingToken, secondClaim.FencingToken)
	}
	if err := store.SaveRunCheckpoint(
		t.Context(),
		firstClaim,
		deliveryservice.SaveCheckpointTx{
			Status: deliverydomain.RunSolving,
			Now:    now.Add(3 * time.Minute),
		},
	); !errors.Is(err, deliveryservice.ErrLeaseLost) {
		t.Fatalf("old worker checkpoint error = %v, want ErrLeaseLost", err)
	}

	replayed, err := store.Replay(t.Context(), deliveryservice.StreamCursor{
		TenantID:      tenantID,
		AggregateType: deliveryservice.AggregateRun,
		AggregateID:   string(run.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := store.Subscribe(t.Context(), deliveryservice.StreamCursor{
		TenantID:      tenantID,
		AggregateType: deliveryservice.AggregateRun,
		AggregateID:   string(run.ID),
		After:         replayed[len(replayed)-1].Seq,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	if err := store.SaveRunCheckpoint(
		t.Context(),
		secondClaim,
		deliveryservice.SaveCheckpointTx{
			Status:           deliverydomain.RunValidating,
			CheckpointDigest: deliveryDigest("7"),
			Now:              now.Add(3 * time.Minute),
		},
	); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-subscription.Events():
		if event.Seq != replayed[len(replayed)-1].Seq+1 {
			t.Fatalf("live event seq = %d", event.Seq)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for live delivery event")
	}
	if _, err := store.GetRun(t.Context(), "other-tenant", run.ID); !errors.Is(
		err,
		deliveryservice.ErrNotFound,
	) {
		t.Fatalf("cross-tenant GetRun error = %v", err)
	}
}

func TestDeliveryStorePublishesRevisionAndAuthorizesArtifacts(t *testing.T) {
	db := openIntegrationDB(t)
	store, err := NewDeliveryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	tenantID := deliverydomain.TenantID("tenant-" + uuid.NewString())
	problem := deliverydomain.ProblemVersion{
		TenantID:         tenantID,
		ProblemID:        "problem-1",
		Version:          1,
		ProblemDigest:    deliveryDigest("1"),
		PolicyDigest:     deliveryDigest("2"),
		CommitmentDigest: deliveryDigest("3"),
		ManifestDigest:   deliveryDigest("4"),
		ProblemArtifact:  deliveryDigest("5"),
		SourceProfile:    "postgres-v1",
		SourceRef:        "cut-1",
		CreatedAt:        now,
	}
	if _, _, err := store.CommitProblem(t.Context(), deliveryservice.CommitProblemTx{
		IdempotencyKey: "problem-key",
		RequestDigest:  deliveryDigest("a"),
		Actor:          deliveryservice.Actor{Subject: "dispatcher-1"},
		Problem:        problem,
	}); err != nil {
		t.Fatal(err)
	}
	run := deliverydomain.OptimizationRun{
		TenantID:       tenantID,
		ID:             "run-1",
		ProblemID:      problem.ProblemID,
		ProblemVersion: 1,
		ProblemDigest:  problem.ProblemDigest,
		SolverProfile:  "solver-v1",
		ConfigDigest:   deliveryDigest("6"),
		Status:         deliverydomain.RunQueued,
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if _, _, err := store.CreateRun(t.Context(), deliveryservice.CreateRunTx{
		IdempotencyKey: "run-key",
		RequestDigest:  deliveryDigest("b"),
		Actor:          deliveryservice.Actor{Subject: "dispatcher-1"},
		Run:            run,
	}); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimRuns(t.Context(), deliveryservice.ClaimRuns{
		TenantID: tenantID,
		WorkerID: "worker-1",
		Limit:    1,
		LeaseTTL: time.Minute,
		Now:      now.Add(time.Minute),
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("ClaimRuns = %+v, %v", claims, err)
	}
	claim := claims[0]
	if err := store.SaveRunCheckpoint(t.Context(), claim, deliveryservice.SaveCheckpointTx{
		Status: deliverydomain.RunValidating,
		Now:    now.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	plan := deliverydomain.DispatchPlan{
		TenantID:      tenantID,
		ID:            "plan-1",
		ProblemID:     problem.ProblemID,
		ActiveVersion: 0,
		CreatedAt:     now.Add(time.Minute),
		UpdatedAt:     now.Add(time.Minute),
	}
	revision := deliverydomain.PlanRevision{
		TenantID:               tenantID,
		ID:                     "revision-1",
		PlanID:                 plan.ID,
		RunID:                  run.ID,
		ProblemDigest:          problem.ProblemDigest,
		PolicyDigest:           problem.PolicyDigest,
		CommitmentDigest:       problem.CommitmentDigest,
		PlanArtifactDigest:     deliveryDigest("7"),
		PlanDigest:             deliveryDigest("8"),
		ValidationArtifact:     deliveryDigest("9"),
		ValidationReportDigest: deliveryDigest("c"),
		Status:                 deliverydomain.RevisionValidated,
		Version:                1,
		CreatedAt:              now.Add(time.Minute),
		UpdatedAt:              now.Add(time.Minute),
	}
	if _, err := store.PublishRevision(t.Context(), claim, deliveryservice.PublishRevisionTx{
		Actor:     deliveryservice.Actor{Subject: "worker-1"},
		Plan:      plan,
		Revision:  revision,
		RunStatus: deliverydomain.RunSucceeded,
		Now:       now.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	completed, err := store.GetRun(t.Context(), tenantID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != deliverydomain.RunSucceeded ||
		completed.ResultRevisionID != revision.ID ||
		completed.ClosedAt == nil {
		t.Fatalf("completed run = %+v", completed)
	}
	for _, digest := range []deliverydomain.ArtifactDigest{
		problem.ProblemArtifact,
		revision.PlanArtifactDigest,
		revision.ValidationArtifact,
	} {
		reachable, err := store.ArtifactReachable(t.Context(), tenantID, digest)
		if err != nil || !reachable {
			t.Fatalf("ArtifactReachable(%s) = %t, %v", digest, reachable, err)
		}
		reachable, err = store.ArtifactReachable(t.Context(), "other-tenant", digest)
		if err != nil || reachable {
			t.Fatalf("cross-tenant ArtifactReachable(%s) = %t, %v",
				digest, reachable, err)
		}
	}
}

func deliveryDigest(seed string) deliverydomain.ArtifactDigest {
	return deliverydomain.ArtifactDigest(strings.Repeat(seed, 64)[:64])
}
