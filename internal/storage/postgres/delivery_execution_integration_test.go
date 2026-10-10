//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	deliveryartifact "github.com/Duang777/waybill-guardian/internal/delivery/artifact"
	deliverydomain "github.com/Duang777/waybill-guardian/internal/delivery/domain"
	deliveryexecution "github.com/Duang777/waybill-guardian/internal/delivery/execution"
	deliveryservice "github.com/Duang777/waybill-guardian/internal/delivery/service"
	deliveryvalidate "github.com/Duang777/waybill-guardian/internal/delivery/validate"
	"github.com/google/uuid"
)

func TestDeliveryExecutionConcurrentConfirmationReconcilesAndActivates(t *testing.T) {
	db := openIntegrationDB(t)
	store, err := NewDeliveryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	tenantID := deliverydomain.TenantID("tenant-" + uuid.NewString())
	plan, revision := createValidatedDeliveryRevision(
		t,
		store,
		tenantID,
		now,
	)
	parameters := json.RawMessage(`{"plan_id":"plan-1","duty_index":0}`)
	parametersDigest, err := deliverydomain.DigestCanonicalJSON(parameters)
	if err != nil {
		t.Fatal(err)
	}
	effectSetDigest := deliveryDigest("d")
	effectArtifact := deliveryDigest("e")
	approval := deliverydomain.PlanApproval{
		TenantID: tenantID,
		ID:       "approval-1",
		Binding: deliverydomain.ApprovalBinding{
			TenantID:               tenantID,
			PlanID:                 plan.ID,
			RevisionID:             revision.ID,
			ActiveVersion:          0,
			ProblemDigest:          revision.ProblemDigest,
			PolicyDigest:           revision.PolicyDigest,
			CommitmentDigest:       revision.CommitmentDigest,
			PlanDigest:             revision.PlanDigest,
			ValidationReportDigest: revision.ValidationReportDigest,
			EffectSetDigest:        effectSetDigest,
		},
		EffectSetArtifact: effectArtifact,
		Status:            deliverydomain.ApprovalPending,
		Version:           1,
		RequestedBy:       "agent:delivery",
		PlanCreatedBy:     "dispatcher-1",
		Reason:            "review validated delivery plan",
		RequestedAt:       now.Add(3 * time.Minute),
		ExpiresAt:         now.Add(time.Hour),
	}
	preview := deliverydomain.EffectPreview{
		ID:                             "effect-1",
		Ordinal:                        0,
		Action:                         deliverydomain.EffectCreateRoute,
		Target:                         "plan-1/duty/0",
		Parameters:                     parameters,
		ParametersDigest:               parametersDigest,
		Required:                       true,
		AdapterID:                      "tms-v1",
		ContractVersion:                "v1",
		KeyRetentionSeconds:            int64((48 * time.Hour) / time.Second),
		LookupConsistencyWindowSeconds: int64(time.Minute / time.Second),
	}
	if _, _, err := store.PrepareApproval(t.Context(), deliveryservice.PrepareApprovalTx{
		IdempotencyKey:          "approval-key",
		RequestDigest:           deliveryDigest("f"),
		ExpectedRevisionVersion: 1,
		Actor:                   deliveryservice.Actor{Subject: "agent:delivery"},
		Approval:                approval,
	}); err != nil {
		t.Fatal(err)
	}
	scriptedAdapter := &scriptedDeliveryAdapter{}
	binding, err := deliveryexecution.NewBinding(
		preview,
		"delivery-effect-v1:"+string(preview.ID),
		now.Add(4*time.Minute),
		scriptedAdapter.Capability(),
	)
	if err != nil {
		t.Fatal(err)
	}
	bindingRaw, err := deliverydomain.CanonicalJSON(binding)
	if err != nil {
		t.Fatal(err)
	}
	bindingDigest, err := deliverydomain.DigestCanonicalJSON(bindingRaw)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := deliveryexecution.NewRegistry(scriptedAdapter)
	if err != nil {
		t.Fatal(err)
	}
	artifactStore, err := deliveryartifact.NewFileStore(t.TempDir(), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	currentTime := binding.CreatedAt
	application, err := deliveryservice.NewPlatform(deliveryservice.PlatformConfig{
		Store:     store,
		Artifacts: artifactStore,
		Sources: deliveryservice.SourceProfiles{
			"unused": unusedProblemSource{},
		},
		Solvers: deliveryservice.SolverProfiles{
			"unused": {
				Solver:       emptyDeliverySolver{},
				ConfigDigest: deliveryDigest("9"),
			},
		},
		Validator: deliveryvalidate.New(deliverydomain.ValidatorIdentity{
			Name:    "delivery-validator",
			Version: "1.0.0",
			Build:   "integration",
		}),
		EffectRegistry: registry,
		Clock:          func() time.Time { return currentTime },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(application.Close)
	effectWorker, err := application.EffectWorker(deliveryservice.EffectWorkerConfig{
		TenantID:     tenantID,
		WorkerID:     "effect-worker-1",
		BatchSize:    1,
		LeaseTTL:     time.Minute,
		PollInterval: time.Millisecond,
		RetryBase:    time.Minute,
		RetryMax:     time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	didWork, err := effectWorker.RunOnce(t.Context())
	if err != nil || didWork || scriptedAdapter.totalCalls() != 0 {
		t.Fatalf(
			"worker before confirmation = work:%t calls:%d err:%v",
			didWork,
			scriptedAdapter.totalCalls(),
			err,
		)
	}
	execution := deliverydomain.DispatchExecution{
		TenantID:        tenantID,
		ID:              "execution-1",
		ApprovalID:      approval.ID,
		PlanID:          plan.ID,
		RevisionID:      revision.ID,
		EffectSetDigest: effectSetDigest,
		Status:          deliverydomain.ExecutionPrepared,
		Version:         1,
		CreatedAt:       binding.CreatedAt,
		UpdatedAt:       binding.CreatedAt,
	}
	effect := deliverydomain.EffectRecord{
		TenantID:                       tenantID,
		ID:                             preview.ID,
		ExecutionID:                    execution.ID,
		RevisionID:                     revision.ID,
		Ordinal:                        preview.Ordinal,
		Action:                         preview.Action,
		Target:                         preview.Target,
		Parameters:                     parameters,
		ParametersDigest:               parametersDigest,
		Required:                       true,
		AdapterID:                      preview.AdapterID,
		ContractVersion:                preview.ContractVersion,
		AdapterBinding:                 bindingRaw,
		AdapterBindingDigest:           bindingDigest,
		IdempotencyKey:                 binding.Key,
		RequestDigest:                  binding.RequestDigest,
		KeyCreatedAt:                   binding.CreatedAt,
		KeyExpiresAt:                   binding.ExpiresAt,
		LookupConsistencyWindowSeconds: int64(time.Minute / time.Second),
		Status:                         deliverydomain.EffectPrepared,
		NextOperation:                  deliverydomain.EffectOperationDispatch,
		UpdatedAt:                      binding.CreatedAt,
	}
	command := deliveryservice.DecideExecutionTx{
		TenantID:                 tenantID,
		IdempotencyKey:           "decision-key",
		RequestDigest:            deliveryDigest("0"),
		Actor:                    deliveryservice.Actor{Subject: "dispatcher-1"},
		ApprovalID:               approval.ID,
		ExpectedVersion:          1,
		ExpectedActiveRevisionID: approval.Binding.BaseRevisionID,
		ExpectedActiveVersion:    approval.Binding.ActiveVersion,
		Decision:                 deliveryservice.ConfirmApproval,
		Execution:                execution,
		Effects:                  []deliverydomain.EffectRecord{effect},
		Now:                      binding.CreatedAt,
	}
	if _, _, err := store.DecideAndCreateExecution(
		t.Context(),
		command,
	); !errors.Is(err, deliveryservice.ErrSeparationOfDuties) {
		t.Fatalf("same-creator decision error = %v, want ErrSeparationOfDuties", err)
	}
	command.OverrideReason = "regional operations emergency override"

	const writers = 20
	results := make(chan deliverydomain.DispatchExecution, writers)
	errs := make(chan error, writers)
	var wait sync.WaitGroup
	wait.Add(writers)
	for range writers {
		go func() {
			defer wait.Done()
			value, _, decideErr := store.DecideAndCreateExecution(
				t.Context(),
				command,
			)
			results <- value
			errs <- decideErr
		}()
	}
	wait.Wait()
	close(results)
	close(errs)
	for decideErr := range errs {
		if decideErr != nil {
			t.Fatal(decideErr)
		}
	}
	for value := range results {
		if value.ID != execution.ID {
			t.Fatalf("execution = %+v", value)
		}
	}
	var executionCount, effectCount int
	if err := db.pool.QueryRow(t.Context(), `
		SELECT count(*) FROM waybill.delivery_executions
		WHERE tenant_id = $1 AND approval_id = $2
	`, tenantID, approval.ID).Scan(&executionCount); err != nil {
		t.Fatal(err)
	}
	if err := db.pool.QueryRow(t.Context(), `
		SELECT count(*) FROM waybill.delivery_effects
		WHERE tenant_id = $1 AND execution_id = $2
	`, tenantID, execution.ID).Scan(&effectCount); err != nil {
		t.Fatal(err)
	}
	if executionCount != 1 || effectCount != 1 {
		t.Fatalf("rows = executions:%d effects:%d", executionCount, effectCount)
	}

	currentTime = now.Add(5 * time.Minute)
	didWork, err = effectWorker.RunOnce(t.Context())
	if err != nil || !didWork {
		t.Fatalf("dispatch worker = work:%t err:%v", didWork, err)
	}
	afterDispatch, effectsAfterDispatch, err := store.GetExecution(
		t.Context(),
		tenantID,
		execution.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if afterDispatch.Status != deliverydomain.ExecutionReconciliationRequired ||
		len(effectsAfterDispatch) != 1 ||
		effectsAfterDispatch[0].Status != deliverydomain.EffectUnknown ||
		effectsAfterDispatch[0].NextOperation != deliverydomain.EffectOperationLookup {
		t.Fatalf(
			"after dispatch = execution:%+v effects:%+v",
			afterDispatch,
			effectsAfterDispatch,
		)
	}
	oldClaim := deliveryservice.EffectClaim{
		Effect:       effectsAfterDispatch[0],
		Operation:    deliverydomain.EffectOperationDispatch,
		WorkerID:     "effect-worker-1",
		FencingToken: effectsAfterDispatch[0].FencingToken,
	}
	currentTime = now.Add(7 * time.Minute)
	didWork, err = effectWorker.RunOnce(t.Context())
	if err != nil || !didWork {
		t.Fatalf("lookup worker = work:%t err:%v", didWork, err)
	}
	if _, err := store.CompleteEffect(
		t.Context(),
		oldClaim,
		deliveryservice.CompleteEffectTx{
			Status:        deliverydomain.EffectSucceeded,
			NextOperation: deliverydomain.EffectOperationLookup,
			ObservedAt:    currentTime,
		},
	); !errors.Is(err, deliveryservice.ErrLeaseLost) {
		t.Fatalf("old claim completion error = %v, want ErrLeaseLost", err)
	}
	dispatchCalls, lookupCalls, keys := scriptedAdapter.snapshot()
	if dispatchCalls != 1 ||
		lookupCalls != 1 ||
		len(keys) != 2 ||
		keys[0] != binding.Key ||
		keys[1] != binding.Key {
		t.Fatalf(
			"adapter calls = dispatch:%d lookup:%d keys:%v",
			dispatchCalls,
			lookupCalls,
			keys,
		)
	}
	active, err := store.GetPlan(t.Context(), tenantID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	finalExecution, effects, err := store.GetExecution(
		t.Context(),
		tenantID,
		execution.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if active.ActiveRevision != revision.ID ||
		active.ActiveVersion != 1 ||
		finalExecution.Status != deliverydomain.ExecutionCommitted ||
		finalExecution.CompletedAt == nil ||
		len(effects) != 1 ||
		effects[0].Status != deliverydomain.EffectSucceeded ||
		effects[0].Attempt != 2 {
		t.Fatalf(
			"final state = plan:%+v execution:%+v effects:%+v",
			active,
			finalExecution,
			effects,
		)
	}
	revisionEvents, err := store.Replay(t.Context(), deliveryservice.StreamCursor{
		TenantID:      tenantID,
		AggregateType: deliveryservice.AggregateRevision,
		AggregateID:   string(revision.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	executionEvents, err := store.Replay(t.Context(), deliveryservice.StreamCursor{
		TenantID:      tenantID,
		AggregateType: deliveryservice.AggregateExecution,
		AggregateID:   string(execution.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertDeliveryEvent(
		t,
		revisionEvents,
		deliveryservice.EventApprovalRequested,
		"agent:delivery",
	)
	assertDeliveryEvent(
		t,
		revisionEvents,
		deliveryservice.EventApprovalDecided,
		"dispatcher-1",
	)
	assertDeliveryEvent(
		t,
		revisionEvents,
		deliveryservice.EventEffectCompleted,
		"system:delivery-effect-worker",
	)
	assertDeliveryEvent(
		t,
		revisionEvents,
		deliveryservice.EventRevisionActivated,
		"system:delivery-effect-worker",
	)
	assertDeliveryEvent(
		t,
		executionEvents,
		deliveryservice.EventExecutionCreated,
		"dispatcher-1",
	)
	assertDeliveryEvent(
		t,
		executionEvents,
		deliveryservice.EventEffectCompleted,
		"system:delivery-effect-worker",
	)
	assertDeliveryEvent(
		t,
		executionEvents,
		deliveryservice.EventRevisionActivated,
		"system:delivery-effect-worker",
	)
}

func TestDeliveryExecutionReservationSerializesCompetingApprovals(t *testing.T) {
	t.Run("unknown reconciles before commit and old binding stays stale", func(t *testing.T) {
		db := openIntegrationDB(t)
		store, err := NewDeliveryStore(db)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, time.October, 10, 16, 0, 0, 0, time.UTC)
		fixture := prepareCompetingDeliveryApprovals(t, db, store, now, 1)
		winner, loser := confirmCompetingDeliveryApprovals(t, store, fixture)

		claim := claimOneDeliveryEffect(
			t,
			store,
			fixture.tenantID,
			"reservation-worker-dispatch",
			now.Add(4*time.Minute),
		)
		if claim.Effect.ExecutionID != fixture.commands[winner].Execution.ID {
			t.Fatalf("claimed execution = %s, want winner %s",
				claim.Effect.ExecutionID,
				fixture.commands[winner].Execution.ID,
			)
		}
		if _, err := store.CompleteEffect(
			t.Context(),
			claim,
			deliveryservice.CompleteEffectTx{
				Status:        deliverydomain.EffectUnknown,
				NextOperation: deliverydomain.EffectOperationLookup,
				ErrorCode:     "transport_after_send",
				ObservedAt:    now.Add(4 * time.Minute),
			},
		); err != nil {
			t.Fatal(err)
		}
		assertDeliveryReservationStatus(
			t,
			db,
			fixture.tenantID,
			fixture.commands[winner].Execution.ID,
			executionReservationReconciliationRequired,
		)
		assertCompetingDeliveryApprovalBlocked(t, store, fixture, loser)

		lookup := claimOneDeliveryEffect(
			t,
			store,
			fixture.tenantID,
			"reservation-worker-lookup",
			now.Add(5*time.Minute),
		)
		if lookup.Operation != deliverydomain.EffectOperationLookup {
			t.Fatalf("reconciliation operation = %s, want lookup", lookup.Operation)
		}
		completion, err := store.CompleteEffect(
			t.Context(),
			lookup,
			deliveryservice.CompleteEffectTx{
				Status:        deliverydomain.EffectSucceeded,
				NextOperation: deliverydomain.EffectOperationLookup,
				ExternalRef:   "route-reconciled",
				ObservedAt:    now.Add(5 * time.Minute),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if !completion.ActivationReady {
			t.Fatal("reconciled execution is not activation ready")
		}
		assertDeliveryReservationStatus(
			t,
			db,
			fixture.tenantID,
			fixture.commands[winner].Execution.ID,
			executionReservationReserved,
		)
		winnerApproval := fixture.approvals[winner]
		winnerExecution := fixture.commands[winner].Execution
		if err := store.ActivateRevision(
			t.Context(),
			deliveryservice.ActivateRevisionTx{
				TenantID:      fixture.tenantID,
				ExecutionID:   winnerExecution.ID,
				PlanID:        winnerExecution.PlanID,
				RevisionID:    winnerExecution.RevisionID,
				ActiveVersion: winnerApproval.Binding.ActiveVersion,
				Now:           now.Add(6 * time.Minute),
			},
		); err != nil {
			t.Fatal(err)
		}
		assertDeliveryReservationStatus(
			t,
			db,
			fixture.tenantID,
			winnerExecution.ID,
			executionReservationCommitted,
		)

		_, _, err = store.DecideAndCreateExecution(
			t.Context(),
			fixture.commands[loser],
		)
		if !errors.Is(err, deliveryservice.ErrApprovalStale) {
			t.Fatalf("old binding retry error = %v, want ErrApprovalStale", err)
		}
		assertNoCompetingDeliveryExecution(t, db, fixture, loser)
	})

	t.Run("partial application keeps competing approval blocked", func(t *testing.T) {
		db := openIntegrationDB(t)
		store, err := NewDeliveryStore(db)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, time.October, 10, 17, 0, 0, 0, time.UTC)
		fixture := prepareCompetingDeliveryApprovals(t, db, store, now, 3)
		winner, loser := confirmCompetingDeliveryApprovals(t, store, fixture)

		first := claimOneDeliveryEffect(
			t,
			store,
			fixture.tenantID,
			"partial-worker-1",
			now.Add(4*time.Minute),
		)
		if _, err := store.CompleteEffect(
			t.Context(),
			first,
			deliveryservice.CompleteEffectTx{
				Status:        deliverydomain.EffectSucceeded,
				NextOperation: first.Operation,
				ExternalRef:   "route-first",
				ObservedAt:    now.Add(4 * time.Minute),
			},
		); err != nil {
			t.Fatal(err)
		}
		second := claimOneDeliveryEffect(
			t,
			store,
			fixture.tenantID,
			"partial-worker-2",
			now.Add(5*time.Minute),
		)
		if _, err := store.CompleteEffect(
			t.Context(),
			second,
			deliveryservice.CompleteEffectTx{
				Status:        deliverydomain.EffectPermanentFailed,
				NextOperation: second.Operation,
				ErrorCode:     "provider_rejected_route",
				ObservedAt:    now.Add(5 * time.Minute),
			},
		); err != nil {
			t.Fatal(err)
		}
		execution, _, err := store.GetExecution(
			t.Context(),
			fixture.tenantID,
			fixture.commands[winner].Execution.ID,
		)
		if err != nil {
			t.Fatal(err)
		}
		if execution.Status != deliverydomain.ExecutionPartiallyApplied {
			t.Fatalf("execution status = %s, want partially_applied", execution.Status)
		}
		assertDeliveryReservationStatus(
			t,
			db,
			fixture.tenantID,
			execution.ID,
			executionReservationReconciliationRequired,
		)
		assertCompetingDeliveryApprovalBlocked(t, store, fixture, loser)
	})
}

func TestDeliveryExecutionReservationResolution(t *testing.T) {
	t.Run("nonterminal and ambiguous effects keep the reservation", func(t *testing.T) {
		db := openIntegrationDB(t)
		store, err := NewDeliveryStore(db)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, time.October, 10, 18, 0, 0, 0, time.UTC)
		fixture := prepareCompetingDeliveryApprovals(t, db, store, now, 1)
		winner, _ := confirmCompetingDeliveryApprovals(t, store, fixture)
		executionID := fixture.commands[winner].Execution.ID

		claim := claimOneDeliveryEffect(
			t,
			store,
			fixture.tenantID,
			"resolution-worker-dispatch",
			now.Add(4*time.Minute),
		)
		if _, err := store.CompleteEffect(
			t.Context(),
			claim,
			deliveryservice.CompleteEffectTx{
				Status:        deliverydomain.EffectUnknown,
				NextOperation: deliverydomain.EffectOperationLookup,
				ErrorCode:     "transport_after_send",
				ObservedAt:    now.Add(4 * time.Minute),
			},
		); err != nil {
			t.Fatal(err)
		}
		execution, _, err := store.GetExecution(
			t.Context(),
			fixture.tenantID,
			executionID,
		)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = store.ResolveExecutionReservation(
			t.Context(),
			deliveryservice.ResolveExecutionReservationTx{
				TenantID:              fixture.tenantID,
				IdempotencyKey:        "resolve-unknown",
				RequestDigest:         deliveryDigest("2"),
				Actor:                 deliveryservice.Actor{Subject: "supervisor-1"},
				ExecutionID:           executionID,
				ExpectedVersion:       execution.Version,
				Reason:                "provider outcome is still unknown",
				CompensationReference: "case-unknown",
				Now:                   now.Add(5 * time.Minute),
			},
		)
		if !errors.Is(err, deliveryservice.ErrConflict) {
			t.Fatalf("unknown resolution error = %v, want ErrConflict", err)
		}

		lookup := claimOneDeliveryEffect(
			t,
			store,
			fixture.tenantID,
			"resolution-worker-lookup",
			now.Add(5*time.Minute),
		)
		retryAt := now.Add(7 * time.Minute)
		if _, err := store.CompleteEffect(
			t.Context(),
			lookup,
			deliveryservice.CompleteEffectTx{
				Status:        deliverydomain.EffectRetryWait,
				NextOperation: deliverydomain.EffectOperationLookup,
				ErrorCode:     "provider_lookup_unavailable",
				RetryAt:       &retryAt,
				ObservedAt:    now.Add(6 * time.Minute),
			},
		); err != nil {
			t.Fatal(err)
		}
		execution, _, err = store.GetExecution(
			t.Context(),
			fixture.tenantID,
			executionID,
		)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = store.ResolveExecutionReservation(
			t.Context(),
			deliveryservice.ResolveExecutionReservationTx{
				TenantID:              fixture.tenantID,
				IdempotencyKey:        "resolve-retry-wait",
				RequestDigest:         deliveryDigest("3"),
				Actor:                 deliveryservice.Actor{Subject: "supervisor-1"},
				ExecutionID:           executionID,
				ExpectedVersion:       execution.Version,
				Reason:                "provider retry has not completed",
				CompensationReference: "case-retry",
				Now:                   now.Add(7 * time.Minute),
			},
		)
		if !errors.Is(err, deliveryservice.ErrConflict) {
			t.Fatalf("retry resolution error = %v, want ErrConflict", err)
		}
		assertDeliveryReservationStatus(
			t,
			db,
			fixture.tenantID,
			executionID,
			executionReservationReconciliationRequired,
		)
		claims, err := store.ClaimEffects(
			t.Context(),
			deliveryservice.ClaimEffects{
				TenantID: fixture.tenantID,
				WorkerID: "resolution-expiry-worker",
				Limit:    10,
				LeaseTTL: time.Minute,
				Now:      now.Add(49 * time.Hour),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(claims) != 0 {
			t.Fatalf("claims after key expiry = %+v", claims)
		}
		execution, effects, err := store.GetExecution(
			t.Context(),
			fixture.tenantID,
			executionID,
		)
		if err != nil {
			t.Fatal(err)
		}
		if execution.Status != deliverydomain.ExecutionManualReview ||
			len(effects) != 1 ||
			effects[0].Status != deliverydomain.EffectManualReview ||
			effects[0].DispatchStartedAt == nil {
			t.Fatalf("expired ambiguous effect = execution:%+v effects:%+v", execution, effects)
		}
		command := deliveryservice.ResolveExecutionReservationTx{
			TenantID:        fixture.tenantID,
			IdempotencyKey:  "resolve-expired-unknown-without-reference",
			RequestDigest:   deliveryDigest("4"),
			Actor:           deliveryservice.Actor{Subject: "supervisor-1"},
			ExecutionID:     executionID,
			ExpectedVersion: execution.Version,
			Reason:          "provider outcome was resolved manually",
			Now:             now.Add(49*time.Hour + time.Minute),
		}
		_, _, err = store.ResolveExecutionReservation(t.Context(), command)
		if !errors.Is(err, deliveryservice.ErrConflict) {
			t.Fatalf("unreferenced ambiguous resolution error = %v, want ErrConflict", err)
		}
		command.IdempotencyKey = "resolve-expired-unknown"
		command.RequestDigest = deliveryDigest("5")
		command.CompensationReference = "ops-case-expired-unknown"
		if _, _, err := store.ResolveExecutionReservation(
			t.Context(),
			command,
		); err != nil {
			t.Fatal(err)
		}
		assertDeliveryReservationStatus(
			t,
			db,
			fixture.tenantID,
			executionID,
			executionReservationReleased,
		)
	})

	t.Run("compensated terminal failure releases the plan", func(t *testing.T) {
		db := openIntegrationDB(t)
		store, err := NewDeliveryStore(db)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, time.October, 10, 19, 0, 0, 0, time.UTC)
		fixture := prepareCompetingDeliveryApprovals(t, db, store, now, 3)
		winner, loser := confirmCompetingDeliveryApprovals(t, store, fixture)
		executionID := fixture.commands[winner].Execution.ID

		first := claimOneDeliveryEffect(
			t,
			store,
			fixture.tenantID,
			"resolution-terminal-worker-1",
			now.Add(4*time.Minute),
		)
		if _, err := store.CompleteEffect(
			t.Context(),
			first,
			deliveryservice.CompleteEffectTx{
				Status:        deliverydomain.EffectSucceeded,
				NextOperation: first.Operation,
				ExternalRef:   "route-created",
				ObservedAt:    now.Add(4 * time.Minute),
			},
		); err != nil {
			t.Fatal(err)
		}
		second := claimOneDeliveryEffect(
			t,
			store,
			fixture.tenantID,
			"resolution-terminal-worker-2",
			now.Add(5*time.Minute),
		)
		if _, err := store.CompleteEffect(
			t.Context(),
			second,
			deliveryservice.CompleteEffectTx{
				Status:        deliverydomain.EffectPermanentFailed,
				NextOperation: second.Operation,
				ErrorCode:     "provider_rejected_route",
				ObservedAt:    now.Add(5 * time.Minute),
			},
		); err != nil {
			t.Fatal(err)
		}
		execution, effects, err := store.GetExecution(
			t.Context(),
			fixture.tenantID,
			executionID,
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(effects) != 3 ||
			effects[0].Status != deliverydomain.EffectSucceeded ||
			effects[1].Status != deliverydomain.EffectPermanentFailed ||
			effects[2].Status != deliverydomain.EffectManualReview ||
			effects[2].ErrorCode != "blocked_by_required_failure" {
			t.Fatalf("terminalized effects = %+v", effects)
		}
		revisionBefore, err := store.GetRevision(
			t.Context(),
			fixture.tenantID,
			execution.RevisionID,
		)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = store.ResolveExecutionReservation(
			t.Context(),
			deliveryservice.ResolveExecutionReservationTx{
				TenantID:        fixture.tenantID,
				IdempotencyKey:  "resolve-without-compensation",
				RequestDigest:   deliveryDigest("4"),
				Actor:           deliveryservice.Actor{Subject: "supervisor-1"},
				ExecutionID:     executionID,
				ExpectedVersion: execution.Version,
				Reason:          "terminal effect review completed",
				Now:             now.Add(6 * time.Minute),
			},
		)
		if !errors.Is(err, deliveryservice.ErrConflict) {
			t.Fatalf("uncompensated resolution error = %v, want ErrConflict", err)
		}

		command := deliveryservice.ResolveExecutionReservationTx{
			TenantID:              fixture.tenantID,
			IdempotencyKey:        "resolve-compensated",
			RequestDigest:         deliveryDigest("5"),
			Actor:                 deliveryservice.Actor{Subject: "supervisor-1"},
			ExecutionID:           executionID,
			ExpectedVersion:       execution.Version,
			Reason:                "created route was cancelled after assignment failed",
			CompensationReference: "ops-case-20261010-42",
			Now:                   now.Add(7 * time.Minute),
		}
		type resolutionResult struct {
			execution deliverydomain.DispatchExecution
			replay    deliveryservice.Replay
			err       error
		}
		const resolverCount = 20
		results := make(chan resolutionResult, resolverCount)
		var wait sync.WaitGroup
		wait.Add(resolverCount)
		for range resolverCount {
			go func() {
				defer wait.Done()
				resolved, replay, resolveErr := store.ResolveExecutionReservation(
					t.Context(),
					command,
				)
				results <- resolutionResult{
					execution: resolved,
					replay:    replay,
					err:       resolveErr,
				}
			}()
		}
		wait.Wait()
		close(results)
		mutations := 0
		replays := 0
		for result := range results {
			if result.err != nil {
				t.Fatal(result.err)
			}
			if result.execution.Status != deliverydomain.ExecutionPartiallyApplied ||
				result.execution.Version != execution.Version+1 {
				t.Fatalf(
					"concurrent resolved execution = %+v replay=%+v",
					result.execution,
					result.replay,
				)
			}
			if result.replay.Replayed {
				replays++
			} else {
				mutations++
			}
		}
		if mutations != 1 || replays != resolverCount-1 {
			t.Fatalf(
				"concurrent resolution mutations=%d replays=%d",
				mutations,
				replays,
			)
		}
		assertDeliveryReservationStatus(
			t,
			db,
			fixture.tenantID,
			executionID,
			executionReservationReleased,
		)
		revisionAfter, err := store.GetRevision(
			t.Context(),
			fixture.tenantID,
			execution.RevisionID,
		)
		if err != nil {
			t.Fatal(err)
		}
		if revisionAfter.Status != revisionBefore.Status ||
			revisionAfter.Version != revisionBefore.Version {
			t.Fatalf(
				"resolution changed terminal revision: before=%+v after=%+v",
				revisionBefore,
				revisionAfter,
			)
		}

		stale := command
		stale.IdempotencyKey = "resolve-stale-version"
		stale.RequestDigest = deliveryDigest("6")
		_, _, err = store.ResolveExecutionReservation(t.Context(), stale)
		if !errors.Is(err, deliveryservice.ErrConflict) {
			t.Fatalf("stale resolution error = %v, want ErrConflict", err)
		}

		executionEvents, err := store.Replay(
			t.Context(),
			deliveryservice.StreamCursor{
				TenantID:      fixture.tenantID,
				AggregateType: deliveryservice.AggregateExecution,
				AggregateID:   string(executionID),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		revisionEvents, err := store.Replay(
			t.Context(),
			deliveryservice.StreamCursor{
				TenantID:      fixture.tenantID,
				AggregateType: deliveryservice.AggregateRevision,
				AggregateID:   string(execution.RevisionID),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		assertSingleDeliveryEvent(
			t,
			executionEvents,
			deliveryservice.EventExecutionReservationResolved,
			"supervisor-1",
		)
		assertSingleDeliveryEvent(
			t,
			revisionEvents,
			deliveryservice.EventExecutionReservationResolved,
			"supervisor-1",
		)

		competing, replay, err := store.DecideAndCreateExecution(
			t.Context(),
			fixture.commands[loser],
		)
		if err != nil {
			t.Fatal(err)
		}
		if replay.Replayed || competing.ID != fixture.commands[loser].Execution.ID {
			t.Fatalf("competing confirmation = %+v replay=%+v", competing, replay)
		}
		assertDeliveryReservationStatus(
			t,
			db,
			fixture.tenantID,
			competing.ID,
			executionReservationReserved,
		)
	})

	t.Run("competing resolution keys use execution CAS", func(t *testing.T) {
		db := openIntegrationDB(t)
		store, err := NewDeliveryStore(db)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, time.October, 10, 20, 0, 0, 0, time.UTC)
		fixture := prepareCompetingDeliveryApprovals(t, db, store, now, 1)
		winner, _ := confirmCompetingDeliveryApprovals(t, store, fixture)
		executionID := fixture.commands[winner].Execution.ID

		claim := claimOneDeliveryEffect(
			t,
			store,
			fixture.tenantID,
			"resolution-cas-worker",
			now.Add(4*time.Minute),
		)
		if _, err := store.CompleteEffect(
			t.Context(),
			claim,
			deliveryservice.CompleteEffectTx{
				Status:        deliverydomain.EffectPermanentFailed,
				NextOperation: claim.Operation,
				ErrorCode:     "provider_rejected_route",
				ObservedAt:    now.Add(4 * time.Minute),
			},
		); err != nil {
			t.Fatal(err)
		}
		execution, _, err := store.GetExecution(
			t.Context(),
			fixture.tenantID,
			executionID,
		)
		if err != nil {
			t.Fatal(err)
		}
		commands := [2]deliveryservice.ResolveExecutionReservationTx{}
		for index := range commands {
			commands[index] = deliveryservice.ResolveExecutionReservationTx{
				TenantID:        fixture.tenantID,
				IdempotencyKey:  deliveryservice.IdempotencyKey(fmt.Sprintf("resolve-cas-%d", index)),
				RequestDigest:   deliveryDigest(fmt.Sprintf("%x", index+7)),
				Actor:           deliveryservice.Actor{Subject: "supervisor-1"},
				ExecutionID:     executionID,
				ExpectedVersion: execution.Version,
				Reason:          "provider rejected the only external write",
				Now:             now.Add(5 * time.Minute),
			}
		}
		results := make(chan error, len(commands))
		var wait sync.WaitGroup
		wait.Add(len(commands))
		for _, command := range commands {
			go func() {
				defer wait.Done()
				_, _, resolveErr := store.ResolveExecutionReservation(
					t.Context(),
					command,
				)
				results <- resolveErr
			}()
		}
		wait.Wait()
		close(results)
		successes := 0
		conflicts := 0
		for resolveErr := range results {
			switch {
			case resolveErr == nil:
				successes++
			case errors.Is(resolveErr, deliveryservice.ErrConflict):
				conflicts++
			default:
				t.Fatal(resolveErr)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf(
				"competing resolutions successes=%d conflicts=%d",
				successes,
				conflicts,
			)
		}
		assertDeliveryReservationStatus(
			t,
			db,
			fixture.tenantID,
			executionID,
			executionReservationReleased,
		)
		events, err := store.Replay(
			t.Context(),
			deliveryservice.StreamCursor{
				TenantID:      fixture.tenantID,
				AggregateType: deliveryservice.AggregateExecution,
				AggregateID:   string(executionID),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		assertSingleDeliveryEvent(
			t,
			events,
			deliveryservice.EventExecutionReservationResolved,
			"supervisor-1",
		)
	})
}

type competingDeliveryApprovals struct {
	tenantID  deliverydomain.TenantID
	plan      deliverydomain.DispatchPlan
	approvals [2]deliverydomain.PlanApproval
	commands  [2]deliveryservice.DecideExecutionTx
}

func prepareCompetingDeliveryApprovals(
	t *testing.T,
	db *DB,
	store *DeliveryStore,
	now time.Time,
	effectCount int,
) competingDeliveryApprovals {
	t.Helper()
	tenantID := deliverydomain.TenantID("tenant-" + uuid.NewString())
	plan, firstRevision := createValidatedDeliveryRevision(
		t,
		store,
		tenantID,
		now,
	)
	secondRevision := createSiblingValidatedDeliveryRevision(
		t,
		db,
		tenantID,
		plan,
		firstRevision,
		now.Add(30*time.Second),
	)
	revisions := [2]deliverydomain.PlanRevision{firstRevision, secondRevision}
	effectSetDigests := [2]deliverydomain.ArtifactDigest{
		deliveryDigest("d"),
		deliveryDigest("e"),
	}
	effectArtifacts := [2]deliverydomain.ArtifactDigest{
		deliveryDigest("e"),
		deliveryDigest("f"),
	}
	requestDigests := [2]deliverydomain.ArtifactDigest{
		deliveryDigest("a"),
		deliveryDigest("b"),
	}
	fixture := competingDeliveryApprovals{
		tenantID: tenantID,
		plan:     plan,
	}
	for index, revision := range revisions {
		approval := deliverydomain.PlanApproval{
			TenantID: tenantID,
			ID: deliverydomain.ApprovalID(
				fmt.Sprintf("competing-approval-%d", index+1),
			),
			Binding: deliverydomain.ApprovalBinding{
				TenantID:               tenantID,
				PlanID:                 plan.ID,
				RevisionID:             revision.ID,
				BaseRevisionID:         plan.ActiveRevision,
				ActiveVersion:          plan.ActiveVersion,
				ProblemDigest:          revision.ProblemDigest,
				PolicyDigest:           revision.PolicyDigest,
				CommitmentDigest:       revision.CommitmentDigest,
				PlanDigest:             revision.PlanDigest,
				ValidationReportDigest: revision.ValidationReportDigest,
				EffectSetDigest:        effectSetDigests[index],
			},
			EffectSetArtifact: effectArtifacts[index],
			Status:            deliverydomain.ApprovalPending,
			Version:           1,
			RequestedBy:       "agent:delivery",
			PlanCreatedBy:     "dispatcher-1",
			Reason:            "compare competing validated revisions",
			RequestedAt:       now.Add(time.Minute),
			ExpiresAt:         now.Add(time.Hour),
		}
		stored, _, err := store.PrepareApproval(
			t.Context(),
			deliveryservice.PrepareApprovalTx{
				IdempotencyKey: deliveryservice.IdempotencyKey(
					fmt.Sprintf("competing-approval-key-%d", index+1),
				),
				RequestDigest:           requestDigests[index],
				ExpectedRevisionVersion: revision.Version,
				Actor:                   deliveryservice.Actor{Subject: approval.RequestedBy},
				Approval:                approval,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		previews := make([]deliverydomain.EffectPreview, 0, effectCount)
		for effectIndex := range effectCount {
			parameters := json.RawMessage(fmt.Sprintf(
				`{"candidate":%d,"duty_index":%d}`,
				index+1,
				effectIndex,
			))
			parametersDigest, err := deliverydomain.DigestCanonicalJSON(parameters)
			if err != nil {
				t.Fatal(err)
			}
			previews = append(previews, deliverydomain.EffectPreview{
				ID: deliverydomain.EffectID(fmt.Sprintf(
					"competing-effect-%d-%d",
					index+1,
					effectIndex+1,
				)),
				Ordinal:                        uint32(effectIndex),
				Action:                         deliverydomain.EffectCreateRoute,
				Target:                         fmt.Sprintf("candidate-%d/duty/%d", index+1, effectIndex),
				Parameters:                     parameters,
				ParametersDigest:               parametersDigest,
				Required:                       true,
				AdapterID:                      "tms-v1",
				ContractVersion:                "v1",
				KeyRetentionSeconds:            int64((48 * time.Hour) / time.Second),
				LookupConsistencyWindowSeconds: int64(time.Minute / time.Second),
			})
		}
		fixture.approvals[index] = stored
		fixture.commands[index] = makeDeliveryConfirmationCommand(
			t,
			stored,
			previews,
			deliverydomain.ExecutionID(fmt.Sprintf("competing-execution-%d", index+1)),
			deliveryservice.IdempotencyKey(fmt.Sprintf("competing-decision-%d", index+1)),
			requestDigests[1-index],
			now.Add(3*time.Minute),
		)
	}
	return fixture
}

func createSiblingValidatedDeliveryRevision(
	t *testing.T,
	db *DB,
	tenantID deliverydomain.TenantID,
	plan deliverydomain.DispatchPlan,
	source deliverydomain.PlanRevision,
	now time.Time,
) deliverydomain.PlanRevision {
	t.Helper()
	revision := deliverydomain.PlanRevision{
		TenantID:               tenantID,
		ID:                     "revision-2",
		PlanID:                 plan.ID,
		RunID:                  "run-2",
		ProblemDigest:          source.ProblemDigest,
		PolicyDigest:           source.PolicyDigest,
		CommitmentDigest:       source.CommitmentDigest,
		PlanArtifactDigest:     deliveryDigest("d"),
		PlanDigest:             deliveryDigest("e"),
		ValidationArtifact:     deliveryDigest("f"),
		ValidationReportDigest: deliveryDigest("0"),
		Status:                 deliverydomain.RevisionValidated,
		Version:                1,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	tx, err := db.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err := tx.Exec(t.Context(), `
		INSERT INTO waybill.delivery_runs (
			tenant_id, run_id, problem_id, problem_version, problem_digest,
			solver_profile, config_digest, requested_by, status, version,
			fencing_token, created_at, updated_at, closed_at
		) VALUES (
			$1, $2, $3, 1, $4,
			'solver-v1', $5, 'dispatcher-1', 'succeeded', 1,
			0, $6, $6, $6
		)
	`, tenantID, revision.RunID, plan.ProblemID, revision.ProblemDigest,
		deliveryDigest("1"), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `
		INSERT INTO waybill.delivery_plan_revisions (
			tenant_id, revision_id, plan_id, run_id,
			problem_digest, policy_digest, commitment_digest,
			plan_artifact_digest, plan_digest,
			validation_artifact_digest, validation_report_digest,
			status, version, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7,
			$8, $9,
			$10, $11,
			'validated', 1, $12, $12
		)
	`, tenantID, revision.ID, revision.PlanID, revision.RunID,
		revision.ProblemDigest, revision.PolicyDigest, revision.CommitmentDigest,
		revision.PlanArtifactDigest, revision.PlanDigest,
		revision.ValidationArtifact, revision.ValidationReportDigest, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `
		UPDATE waybill.delivery_runs
		SET result_revision_id = $3
		WHERE tenant_id = $1 AND run_id = $2
	`, tenantID, revision.RunID, revision.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return revision
}

func makeDeliveryConfirmationCommand(
	t *testing.T,
	approval deliverydomain.PlanApproval,
	previews []deliverydomain.EffectPreview,
	executionID deliverydomain.ExecutionID,
	idempotencyKey deliveryservice.IdempotencyKey,
	requestDigest deliverydomain.ArtifactDigest,
	now time.Time,
) deliveryservice.DecideExecutionTx {
	t.Helper()
	execution := deliverydomain.DispatchExecution{
		TenantID:        approval.TenantID,
		ID:              executionID,
		ApprovalID:      approval.ID,
		PlanID:          approval.Binding.PlanID,
		RevisionID:      approval.Binding.RevisionID,
		EffectSetDigest: approval.Binding.EffectSetDigest,
		Status:          deliverydomain.ExecutionPrepared,
		Version:         1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	adapter := &scriptedDeliveryAdapter{}
	effects := make([]deliverydomain.EffectRecord, 0, len(previews))
	for _, preview := range previews {
		binding, err := deliveryexecution.NewBinding(
			preview,
			"delivery-effect-v1:"+string(preview.ID),
			now,
			adapter.Capability(),
		)
		if err != nil {
			t.Fatal(err)
		}
		bindingRaw, err := deliverydomain.CanonicalJSON(binding)
		if err != nil {
			t.Fatal(err)
		}
		bindingDigest, err := deliverydomain.DigestCanonicalJSON(bindingRaw)
		if err != nil {
			t.Fatal(err)
		}
		effects = append(effects, deliverydomain.EffectRecord{
			TenantID:                       approval.TenantID,
			ID:                             preview.ID,
			ExecutionID:                    execution.ID,
			RevisionID:                     execution.RevisionID,
			Ordinal:                        preview.Ordinal,
			Action:                         preview.Action,
			Target:                         preview.Target,
			Parameters:                     preview.Parameters,
			ParametersDigest:               preview.ParametersDigest,
			Required:                       preview.Required,
			AdapterID:                      preview.AdapterID,
			ContractVersion:                preview.ContractVersion,
			AdapterBinding:                 bindingRaw,
			AdapterBindingDigest:           bindingDigest,
			IdempotencyKey:                 binding.Key,
			RequestDigest:                  binding.RequestDigest,
			KeyCreatedAt:                   binding.CreatedAt,
			KeyExpiresAt:                   binding.ExpiresAt,
			LookupConsistencyWindowSeconds: preview.LookupConsistencyWindowSeconds,
			Status:                         deliverydomain.EffectPrepared,
			NextOperation:                  deliverydomain.EffectOperationDispatch,
			UpdatedAt:                      now,
		})
	}
	return deliveryservice.DecideExecutionTx{
		TenantID:                 approval.TenantID,
		IdempotencyKey:           idempotencyKey,
		RequestDigest:            requestDigest,
		Actor:                    deliveryservice.Actor{Subject: "supervisor-1"},
		ApprovalID:               approval.ID,
		ExpectedVersion:          approval.Version,
		ExpectedActiveRevisionID: approval.Binding.BaseRevisionID,
		ExpectedActiveVersion:    approval.Binding.ActiveVersion,
		Decision:                 deliveryservice.ConfirmApproval,
		Execution:                execution,
		Effects:                  effects,
		Now:                      now,
	}
}

func confirmCompetingDeliveryApprovals(
	t *testing.T,
	store *DeliveryStore,
	fixture competingDeliveryApprovals,
) (int, int) {
	t.Helper()
	type confirmationResult struct {
		index int
		err   error
	}
	start := make(chan struct{})
	results := make(chan confirmationResult, len(fixture.commands))
	var wait sync.WaitGroup
	for index, command := range fixture.commands {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, _, err := store.DecideAndCreateExecution(t.Context(), command)
			results <- confirmationResult{index: index, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	winner := -1
	loser := -1
	for result := range results {
		switch {
		case result.err == nil:
			if winner != -1 {
				t.Fatal("both competing approvals acquired a reservation")
			}
			winner = result.index
		case errors.Is(result.err, deliveryservice.ErrConflict):
			loser = result.index
		default:
			t.Fatalf("competing approval %d error = %v", result.index, result.err)
		}
	}
	if winner == -1 || loser == -1 {
		t.Fatalf("competition result = winner:%d loser:%d", winner, loser)
	}

	var reservations, executions, effects int
	if err := store.db.pool.QueryRow(t.Context(), `
		SELECT (
			SELECT count(*)
			FROM waybill.delivery_execution_reservations
			WHERE tenant_id = $1 AND plan_id = $2
		), (
			SELECT count(*)
			FROM waybill.delivery_executions
			WHERE tenant_id = $1 AND plan_id = $2
		), (
			SELECT count(*)
			FROM waybill.delivery_effects
			WHERE tenant_id = $1
		)
	`, fixture.tenantID, fixture.plan.ID).Scan(
		&reservations,
		&executions,
		&effects,
	); err != nil {
		t.Fatal(err)
	}
	if reservations != 1 ||
		executions != 1 ||
		effects != len(fixture.commands[winner].Effects) {
		t.Fatalf(
			"winner rows = reservations:%d executions:%d effects:%d",
			reservations,
			executions,
			effects,
		)
	}
	assertNoCompetingDeliveryExecution(t, store.db, fixture, loser)
	return winner, loser
}

func assertCompetingDeliveryApprovalBlocked(
	t *testing.T,
	store *DeliveryStore,
	fixture competingDeliveryApprovals,
	loser int,
) {
	t.Helper()
	_, _, err := store.DecideAndCreateExecution(t.Context(), fixture.commands[loser])
	if !errors.Is(err, deliveryservice.ErrConflict) {
		t.Fatalf("competing retry error = %v, want ErrConflict", err)
	}
	assertNoCompetingDeliveryExecution(t, store.db, fixture, loser)
}

func assertNoCompetingDeliveryExecution(
	t *testing.T,
	db *DB,
	fixture competingDeliveryApprovals,
	index int,
) {
	t.Helper()
	command := fixture.commands[index]
	var reservations, executions, effects int
	if err := db.pool.QueryRow(t.Context(), `
		SELECT (
			SELECT count(*)
			FROM waybill.delivery_execution_reservations
			WHERE tenant_id = $1 AND execution_id = $2
		), (
			SELECT count(*)
			FROM waybill.delivery_executions
			WHERE tenant_id = $1 AND execution_id = $2
		), (
			SELECT count(*)
			FROM waybill.delivery_effects
			WHERE tenant_id = $1 AND execution_id = $2
		)
	`, fixture.tenantID, command.Execution.ID).Scan(
		&reservations,
		&executions,
		&effects,
	); err != nil {
		t.Fatal(err)
	}
	if reservations != 0 || executions != 0 || effects != 0 {
		t.Fatalf(
			"loser rows = reservations:%d executions:%d effects:%d",
			reservations,
			executions,
			effects,
		)
	}
}

func assertDeliveryReservationStatus(
	t *testing.T,
	db *DB,
	tenantID deliverydomain.TenantID,
	executionID deliverydomain.ExecutionID,
	want executionReservationStatus,
) {
	t.Helper()
	var got executionReservationStatus
	if err := db.pool.QueryRow(t.Context(), `
		SELECT status
		FROM waybill.delivery_execution_reservations
		WHERE tenant_id = $1 AND execution_id = $2
	`, tenantID, executionID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("reservation status = %s, want %s", got, want)
	}
}

func assertDeliveryEvent(
	t *testing.T,
	events []deliveryservice.Event,
	eventType deliveryservice.EventType,
	actor string,
) {
	t.Helper()
	for _, event := range events {
		if event.Type == eventType && event.Actor == actor {
			return
		}
	}
	t.Fatalf("event %q by %q not found in %+v", eventType, actor, events)
}

func assertSingleDeliveryEvent(
	t *testing.T,
	events []deliveryservice.Event,
	eventType deliveryservice.EventType,
	actor string,
) {
	t.Helper()
	count := 0
	for _, event := range events {
		if event.Type == eventType && event.Actor == actor {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("event %q by %q count = %d, want 1", eventType, actor, count)
	}
}

func createValidatedDeliveryRevision(
	t *testing.T,
	store *DeliveryStore,
	tenantID deliverydomain.TenantID,
	now time.Time,
) (deliverydomain.DispatchPlan, deliverydomain.PlanRevision) {
	t.Helper()
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
		RequestDigest:  deliveryDigest("6"),
		Actor:          deliveryservice.Actor{Subject: "dispatcher-1"},
		Problem:        problem,
	}); err != nil {
		t.Fatal(err)
	}
	run := deliverydomain.OptimizationRun{
		TenantID:       tenantID,
		ID:             "run-1",
		ProblemID:      problem.ProblemID,
		ProblemVersion: problem.Version,
		ProblemDigest:  problem.ProblemDigest,
		SolverProfile:  "solver-v1",
		ConfigDigest:   deliveryDigest("7"),
		RequestedBy:    "dispatcher-1",
		Status:         deliverydomain.RunQueued,
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if _, _, err := store.CreateRun(t.Context(), deliveryservice.CreateRunTx{
		IdempotencyKey: "run-key",
		RequestDigest:  deliveryDigest("8"),
		Actor:          deliveryservice.Actor{Subject: run.RequestedBy},
		Run:            run,
	}); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimRuns(t.Context(), deliveryservice.ClaimRuns{
		TenantID: tenantID,
		WorkerID: "run-worker-1",
		Limit:    1,
		LeaseTTL: time.Minute,
		Now:      now.Add(time.Minute),
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("ClaimRuns = %+v, %v", claims, err)
	}
	if err := store.SaveRunCheckpoint(
		t.Context(),
		claims[0],
		deliveryservice.SaveCheckpointTx{
			Status: deliverydomain.RunValidating,
			Now:    now.Add(time.Minute),
		},
	); err != nil {
		t.Fatal(err)
	}
	plan := deliverydomain.DispatchPlan{
		TenantID:      tenantID,
		ID:            "plan-1",
		ProblemID:     problem.ProblemID,
		ActiveVersion: 0,
		CreatedAt:     now.Add(2 * time.Minute),
		UpdatedAt:     now.Add(2 * time.Minute),
	}
	revision := deliverydomain.PlanRevision{
		TenantID:               tenantID,
		ID:                     "revision-1",
		PlanID:                 plan.ID,
		RunID:                  run.ID,
		ProblemDigest:          problem.ProblemDigest,
		PolicyDigest:           problem.PolicyDigest,
		CommitmentDigest:       problem.CommitmentDigest,
		PlanArtifactDigest:     deliveryDigest("9"),
		PlanDigest:             deliveryDigest("a"),
		ValidationArtifact:     deliveryDigest("b"),
		ValidationReportDigest: deliveryDigest("c"),
		Status:                 deliverydomain.RevisionValidated,
		Version:                1,
		CreatedAt:              now.Add(2 * time.Minute),
		UpdatedAt:              now.Add(2 * time.Minute),
	}
	if _, err := store.PublishRevision(
		t.Context(),
		claims[0],
		deliveryservice.PublishRevisionTx{
			Actor:     deliveryservice.Actor{Subject: "run-worker-1"},
			Plan:      plan,
			Revision:  revision,
			RunStatus: deliverydomain.RunSucceeded,
			Now:       now.Add(2 * time.Minute),
		},
	); err != nil {
		t.Fatal(err)
	}
	return plan, revision
}

type unusedProblemSource struct{}

func (unusedProblemSource) BuildProblem(
	context.Context,
	deliveryservice.SourceBuildRequest,
) (deliveryservice.SourceBuildResult, error) {
	return deliveryservice.SourceBuildResult{}, errors.New("unused")
}

type scriptedDeliveryAdapter struct {
	mu            sync.Mutex
	dispatchCalls int
	lookupCalls   int
	keys          []string
}

func (*scriptedDeliveryAdapter) Capability() deliveryexecution.Capability {
	return deliveryexecution.Capability{
		AdapterID:               "tms-v1",
		ContractVersion:         "v1",
		Actions:                 []deliverydomain.EffectAction{deliverydomain.EffectCreateRoute},
		SameRequestReplays:      true,
		MismatchRejected:        true,
		LookupByKey:             true,
		KeyRetention:            48 * time.Hour,
		LookupConsistencyWindow: time.Minute,
		SupportsRecovery:        true,
	}
}

func (*scriptedDeliveryAdapter) Bind(
	preview deliverydomain.EffectPreview,
	key string,
	createdAt time.Time,
) (deliveryexecution.Binding, error) {
	return deliveryexecution.NewBinding(
		preview,
		key,
		createdAt,
		(&scriptedDeliveryAdapter{}).Capability(),
	)
}

func (adapter *scriptedDeliveryAdapter) Dispatch(
	_ context.Context,
	binding deliveryexecution.Binding,
) (deliveryexecution.Result, error) {
	adapter.mu.Lock()
	adapter.dispatchCalls++
	adapter.keys = append(adapter.keys, binding.Key)
	adapter.mu.Unlock()
	return deliveryexecution.Result{
		Disposition: deliveryexecution.DispositionUnknown,
		Response:    []byte(`{"status":"accepted"}`),
		ErrorCode:   "transport_after_send",
	}, nil
}

func (adapter *scriptedDeliveryAdapter) Lookup(
	_ context.Context,
	binding deliveryexecution.Binding,
	_ time.Time,
) (deliveryexecution.Result, error) {
	adapter.mu.Lock()
	adapter.lookupCalls++
	adapter.keys = append(adapter.keys, binding.Key)
	adapter.mu.Unlock()
	return deliveryexecution.Result{
		Disposition: deliveryexecution.DispositionSucceeded,
		ExternalRef: "route-42",
		Response:    []byte(`{"status":"applied"}`),
	}, nil
}

func (adapter *scriptedDeliveryAdapter) totalCalls() int {
	dispatch, lookup, _ := adapter.snapshot()
	return dispatch + lookup
}

func (adapter *scriptedDeliveryAdapter) snapshot() (int, int, []string) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return adapter.dispatchCalls, adapter.lookupCalls, append([]string(nil), adapter.keys...)
}

var (
	_ deliveryservice.ProblemSource = unusedProblemSource{}
	_ deliveryexecution.Adapter     = (*scriptedDeliveryAdapter)(nil)
)
