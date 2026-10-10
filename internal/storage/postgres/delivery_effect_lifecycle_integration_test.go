//go:build integration

package postgres

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	deliveryartifact "github.com/Duang777/waybill-guardian/internal/delivery/artifact"
	deliverydomain "github.com/Duang777/waybill-guardian/internal/delivery/domain"
	deliveryexecution "github.com/Duang777/waybill-guardian/internal/delivery/execution"
	deliveryservice "github.com/Duang777/waybill-guardian/internal/delivery/service"
	deliveryvalidate "github.com/Duang777/waybill-guardian/internal/delivery/validate"
	"github.com/google/uuid"
)

func TestDeliveryEffectPermanentFailureAfterSuccessIsPartiallyApplied(
	t *testing.T,
) {
	db := openIntegrationDB(t)
	store, err := NewDeliveryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 10, 12, 0, 0, 0, time.UTC)
	tenantID := deliverydomain.TenantID("tenant-" + uuid.NewString())
	plan, revision, approval, previews := prepareDeliveryApprovalFixture(
		t,
		store,
		tenantID,
		now,
		now.Add(time.Hour),
		3,
	)
	execution := confirmDeliveryExecutionFixture(
		t,
		store,
		approval,
		previews,
		now.Add(2*time.Minute),
	)

	first := claimOneDeliveryEffect(
		t,
		store,
		tenantID,
		"effect-worker-1",
		now.Add(3*time.Minute),
	)
	if first.Effect.Ordinal != 0 {
		t.Fatalf("first claimed ordinal = %d, want 0", first.Effect.Ordinal)
	}
	firstCompletion, err := store.CompleteEffect(
		t.Context(),
		first,
		deliveryservice.CompleteEffectTx{
			Status:         deliverydomain.EffectSucceeded,
			NextOperation:  first.Operation,
			ExternalRef:    "route-42",
			ResponseDigest: deliveryDigest("1"),
			ObservedAt:     now.Add(3 * time.Minute),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if firstCompletion.ActivationReady {
		t.Fatal("activation became ready with one required effect outstanding")
	}

	second := claimOneDeliveryEffect(
		t,
		store,
		tenantID,
		"effect-worker-2",
		now.Add(4*time.Minute),
	)
	if second.Effect.Ordinal != 1 {
		t.Fatalf("second claimed ordinal = %d, want 1", second.Effect.Ordinal)
	}
	secondCompletion, err := store.CompleteEffect(
		t.Context(),
		second,
		deliveryservice.CompleteEffectTx{
			Status:        deliverydomain.EffectPermanentFailed,
			NextOperation: second.Operation,
			ErrorCode:     "provider_rejected_route",
			ObservedAt:    now.Add(4 * time.Minute),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if secondCompletion.ActivationReady {
		t.Fatal("activation became ready after a required effect failed")
	}

	storedPlan, err := store.GetPlan(t.Context(), tenantID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedRevision, err := store.GetRevision(t.Context(), tenantID, revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedExecution, effects, err := store.GetExecution(
		t.Context(),
		tenantID,
		execution.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	remainingClaims, err := store.ClaimEffects(
		t.Context(),
		deliveryservice.ClaimEffects{
			TenantID: tenantID,
			WorkerID: "effect-worker-3",
			Limit:    10,
			LeaseTTL: time.Minute,
			Now:      now.Add(5 * time.Minute),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(remainingClaims) != 0 {
		t.Fatalf("claimed %d effects after required failure", len(remainingClaims))
	}
	if storedPlan.ActiveRevision != "" ||
		storedPlan.ActiveVersion != 0 ||
		storedRevision.Status != deliverydomain.RevisionPartiallyApplied ||
		storedExecution.Status != deliverydomain.ExecutionPartiallyApplied ||
		storedExecution.CompletedAt != nil ||
		len(effects) != 3 ||
		effects[0].Status != deliverydomain.EffectSucceeded ||
		effects[1].Status != deliverydomain.EffectPermanentFailed ||
		effects[2].Status != deliverydomain.EffectPrepared {
		t.Fatalf(
			"partial state = plan:%+v revision:%+v execution:%+v effects:%+v",
			storedPlan,
			storedRevision,
			storedExecution,
			effects,
		)
	}
	assertDeliveryReservationStatus(
		t,
		db,
		tenantID,
		execution.ID,
		executionReservationReconciliationRequired,
	)
}

func TestDeliveryPreparedEffectWithExpiredKeyMovesToManualReview(t *testing.T) {
	db := openIntegrationDB(t)
	store, err := NewDeliveryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 10, 12, 30, 0, 0, time.UTC)
	tenantID := deliverydomain.TenantID("tenant-" + uuid.NewString())
	_, revision, approval, previews := prepareDeliveryApprovalFixture(
		t,
		store,
		tenantID,
		now,
		now.Add(time.Hour),
		1,
	)
	execution := confirmDeliveryExecutionFixture(
		t,
		store,
		approval,
		previews,
		now.Add(2*time.Minute),
	)

	claims, err := store.ClaimEffects(t.Context(), deliveryservice.ClaimEffects{
		TenantID: tenantID,
		WorkerID: "effect-worker-1",
		Limit:    10,
		LeaseTTL: time.Minute,
		Now:      now.Add(49 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 0 {
		t.Fatalf("claimed expired effects = %d, want 0", len(claims))
	}
	storedRevision, err := store.GetRevision(t.Context(), tenantID, revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedExecution, effects, err := store.GetExecution(
		t.Context(),
		tenantID,
		execution.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if storedRevision.Status != deliverydomain.RevisionPartiallyApplied ||
		storedExecution.Status != deliverydomain.ExecutionManualReview ||
		len(effects) != 1 ||
		effects[0].Status != deliverydomain.EffectManualReview ||
		effects[0].Attempt != 0 ||
		effects[0].DispatchStartedAt != nil ||
		effects[0].ErrorCode != "idempotency_key_expired" {
		t.Fatalf(
			"expired state = revision:%+v execution:%+v effects:%+v",
			storedRevision,
			storedExecution,
			effects,
		)
	}
	assertDeliveryReservationStatus(
		t,
		db,
		tenantID,
		execution.ID,
		executionReservationReleased,
	)
}

func TestDeliveryOptionalReconciliationDoesNotBlockRequiredEffect(t *testing.T) {
	db := openIntegrationDB(t)
	store, err := NewDeliveryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 10, 12, 45, 0, 0, time.UTC)
	tenantID := deliverydomain.TenantID("tenant-" + uuid.NewString())
	_, _, approval, previews := prepareDeliveryApprovalFixture(
		t,
		store,
		tenantID,
		now,
		now.Add(time.Hour),
		3,
	)
	previews[1].Required = false
	execution := confirmDeliveryExecutionFixture(
		t,
		store,
		approval,
		previews,
		now.Add(2*time.Minute),
	)

	first := claimOneDeliveryEffect(
		t,
		store,
		tenantID,
		"effect-worker-1",
		now.Add(3*time.Minute),
	)
	if _, err := store.CompleteEffect(
		t.Context(),
		first,
		deliveryservice.CompleteEffectTx{
			Status:        deliverydomain.EffectSucceeded,
			NextOperation: first.Operation,
			ExternalRef:   "route-1",
			ObservedAt:    now.Add(3 * time.Minute),
		},
	); err != nil {
		t.Fatal(err)
	}

	optional := claimOneDeliveryEffect(
		t,
		store,
		tenantID,
		"effect-worker-2",
		now.Add(4*time.Minute),
	)
	if optional.Effect.Ordinal != 1 || optional.Effect.Required {
		t.Fatalf("optional claim = %+v", optional.Effect)
	}
	retryAt := now.Add(time.Hour)
	if _, err := store.CompleteEffect(
		t.Context(),
		optional,
		deliveryservice.CompleteEffectTx{
			Status:        deliverydomain.EffectUnknown,
			NextOperation: deliverydomain.EffectOperationLookup,
			ErrorCode:     "transport_after_send",
			RetryAt:       &retryAt,
			ObservedAt:    now.Add(4 * time.Minute),
		},
	); err != nil {
		t.Fatal(err)
	}
	assertDeliveryReservationStatus(
		t,
		db,
		tenantID,
		execution.ID,
		executionReservationReserved,
	)

	required := claimOneDeliveryEffect(
		t,
		store,
		tenantID,
		"effect-worker-3",
		now.Add(5*time.Minute),
	)
	if required.Effect.Ordinal != 2 || !required.Effect.Required {
		t.Fatalf("required claim = %+v", required.Effect)
	}
	if _, err := store.CompleteEffect(
		t.Context(),
		required,
		deliveryservice.CompleteEffectTx{
			Status:        deliverydomain.EffectPermanentFailed,
			NextOperation: required.Operation,
			ErrorCode:     "provider_rejected",
			ObservedAt:    now.Add(5 * time.Minute),
		},
	); err != nil {
		t.Fatal(err)
	}
	reconciliation := claimOneDeliveryEffect(
		t,
		store,
		tenantID,
		"effect-worker-4",
		retryAt.Add(time.Minute),
	)
	if reconciliation.Effect.ID != optional.Effect.ID ||
		reconciliation.Operation != deliverydomain.EffectOperationLookup {
		t.Fatalf("optional reconciliation after required failure = %+v", reconciliation)
	}
}

func TestDeliveryExpiredOptionalEffectDoesNotBlockRequiredActivation(t *testing.T) {
	db := openIntegrationDB(t)
	store, err := NewDeliveryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 10, 12, 50, 0, 0, time.UTC)
	tenantID := deliverydomain.TenantID("tenant-" + uuid.NewString())
	plan, revision, approval, previews := prepareDeliveryApprovalFixture(
		t,
		store,
		tenantID,
		now,
		now.Add(time.Hour),
		3,
	)
	previews[1].Required = false
	execution := confirmDeliveryExecutionFixture(
		t,
		store,
		approval,
		previews,
		now.Add(2*time.Minute),
	)
	if _, err := db.pool.Exec(t.Context(), `
		UPDATE waybill.delivery_effects
		SET key_expires_at = $3
		WHERE tenant_id = $1 AND effect_id = $2
	`, tenantID, previews[1].ID, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}

	first := claimOneDeliveryEffect(
		t,
		store,
		tenantID,
		"effect-worker-1",
		now.Add(3*time.Minute),
	)
	if _, err := store.CompleteEffect(
		t.Context(),
		first,
		deliveryservice.CompleteEffectTx{
			Status:        deliverydomain.EffectSucceeded,
			NextOperation: first.Operation,
			ExternalRef:   "route-1",
			ObservedAt:    now.Add(3 * time.Minute),
		},
	); err != nil {
		t.Fatal(err)
	}

	claims, err := store.ClaimEffects(t.Context(), deliveryservice.ClaimEffects{
		TenantID: tenantID,
		WorkerID: "effect-worker-2",
		Limit:    10,
		LeaseTTL: time.Minute,
		Now:      now.Add(6 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 ||
		claims[0].Effect.Ordinal != 2 ||
		!claims[0].Effect.Required {
		t.Fatalf("required claim after optional expiry = %+v", claims)
	}
	completed, err := store.CompleteEffect(
		t.Context(),
		claims[0],
		deliveryservice.CompleteEffectTx{
			Status:        deliverydomain.EffectSucceeded,
			NextOperation: claims[0].Operation,
			ExternalRef:   "route-3",
			ObservedAt:    now.Add(6 * time.Minute),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !completed.ActivationReady {
		t.Fatal("required effects should be ready for activation")
	}
	if err := store.ActivateRevision(t.Context(), deliveryservice.ActivateRevisionTx{
		TenantID:      tenantID,
		ExecutionID:   execution.ID,
		PlanID:        plan.ID,
		RevisionID:    revision.ID,
		ActiveVersion: approval.Binding.ActiveVersion,
		Now:           now.Add(7 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	storedPlan, err := store.GetPlan(t.Context(), tenantID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedRevision, err := store.GetRevision(t.Context(), tenantID, revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedExecution, effects, err := store.GetExecution(
		t.Context(),
		tenantID,
		execution.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if storedPlan.ActiveRevision != revision.ID ||
		storedRevision.Status != deliverydomain.RevisionActive ||
		storedExecution.Status != deliverydomain.ExecutionCommitted ||
		len(effects) != 3 ||
		effects[1].Status != deliverydomain.EffectManualReview ||
		effects[1].ErrorCode != "idempotency_key_expired" {
		t.Fatalf(
			"optional expiry state = plan:%+v revision:%+v execution:%+v effects:%+v",
			storedPlan,
			storedRevision,
			storedExecution,
			effects,
		)
	}
	assertDeliveryReservationStatus(
		t,
		db,
		tenantID,
		execution.ID,
		executionReservationCommitted,
	)
}

func TestDeliveryApprovalExpiryAndDigestStaleness(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		db := openIntegrationDB(t)
		store, err := NewDeliveryStore(db)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, time.October, 10, 13, 0, 0, 0, time.UTC)
		tenantID := deliverydomain.TenantID("tenant-" + uuid.NewString())
		_, revision, approval, _ := prepareDeliveryApprovalFixture(
			t,
			store,
			tenantID,
			now,
			now.Add(time.Hour),
			1,
		)

		count, err := store.ExpireApprovals(t.Context(), deliveryservice.ExpireApprovals{
			TenantID: tenantID,
			Limit:    10,
			Now:      now.Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("expired approvals = %d, want 1", count)
		}
		assertDeliveryApprovalState(
			t,
			store,
			tenantID,
			approval.ID,
			revision.ID,
			deliverydomain.ApprovalExpired,
			deliverydomain.RevisionExpired,
		)
	})

	t.Run("digest drift is stale", func(t *testing.T) {
		db := openIntegrationDB(t)
		store, err := NewDeliveryStore(db)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, time.October, 10, 14, 0, 0, 0, time.UTC)
		tenantID := deliverydomain.TenantID("tenant-" + uuid.NewString())
		_, revision, approval, _ := prepareDeliveryApprovalFixture(
			t,
			store,
			tenantID,
			now,
			now.Add(time.Hour),
			1,
		)
		if _, err := db.pool.Exec(t.Context(), `
			UPDATE waybill.delivery_plan_revisions
			SET plan_digest = $3
			WHERE tenant_id = $1 AND revision_id = $2
		`, tenantID, revision.ID, deliveryDigest("f")); err != nil {
			t.Fatal(err)
		}

		_, _, err = store.DecideAndCreateExecution(
			t.Context(),
			deliveryservice.DecideExecutionTx{
				TenantID:                 tenantID,
				IdempotencyKey:           "reject-stale-approval",
				RequestDigest:            deliveryDigest("e"),
				Actor:                    deliveryservice.Actor{Subject: "supervisor-1"},
				ApprovalID:               approval.ID,
				ExpectedVersion:          1,
				ExpectedActiveRevisionID: approval.Binding.BaseRevisionID,
				ExpectedActiveVersion:    approval.Binding.ActiveVersion,
				Decision:                 deliveryservice.RejectApproval,
				RejectReason:             "superseded request",
				Now:                      now.Add(10 * time.Minute),
			},
		)
		if !errors.Is(err, deliveryservice.ErrApprovalStale) {
			t.Fatalf("decision error = %v, want ErrApprovalStale", err)
		}

		count, err := store.ExpireApprovals(t.Context(), deliveryservice.ExpireApprovals{
			TenantID: tenantID,
			Limit:    10,
			Now:      now.Add(10 * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("stale approvals = %d, want 1", count)
		}
		assertDeliveryApprovalState(
			t,
			store,
			tenantID,
			approval.ID,
			revision.ID,
			deliverydomain.ApprovalStale,
			deliverydomain.RevisionStale,
		)
	})
}

func TestDeliveryActivationCASFailureBecomesPartialApplication(t *testing.T) {
	db := openIntegrationDB(t)
	store, err := NewDeliveryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 10, 15, 0, 0, 0, time.UTC)
	tenantID := deliverydomain.TenantID("tenant-" + uuid.NewString())
	plan, revision, approval, previews := prepareDeliveryApprovalFixture(
		t,
		store,
		tenantID,
		now,
		now.Add(time.Hour),
		1,
	)
	execution := confirmDeliveryExecutionFixture(
		t,
		store,
		approval,
		previews,
		now.Add(2*time.Minute),
	)
	claim := claimOneDeliveryEffect(
		t,
		store,
		tenantID,
		"effect-worker-1",
		now.Add(3*time.Minute),
	)
	completion, err := store.CompleteEffect(
		t.Context(),
		claim,
		deliveryservice.CompleteEffectTx{
			Status:        deliverydomain.EffectSucceeded,
			NextOperation: claim.Operation,
			ExternalRef:   "route-42",
			ObservedAt:    now.Add(3 * time.Minute),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !completion.ActivationReady {
		t.Fatal("activation did not become ready")
	}
	activateCompetingDeliveryRevision(
		t,
		db,
		tenantID,
		plan,
		revision,
		now.Add(7*time.Minute/2),
	)

	artifactStore, err := deliveryartifact.NewFileStore(t.TempDir(), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
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
		Clock: func() time.Time { return now.Add(4 * time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(application.Close)
	if err := application.Recover(t.Context()); err != nil {
		t.Fatalf("recover stale activation: %v", err)
	}
	storedApproval, err := store.GetApproval(t.Context(), tenantID, approval.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedPlan, err := store.GetPlan(t.Context(), tenantID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedRevision, err := store.GetRevision(t.Context(), tenantID, revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedExecution, _, err := store.GetExecution(
		t.Context(),
		tenantID,
		execution.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if storedApproval.Status != deliverydomain.ApprovalStale ||
		storedPlan.ActiveRevision != "revision-competing" ||
		storedPlan.ActiveVersion != 1 ||
		storedRevision.Status != deliverydomain.RevisionPartiallyApplied ||
		storedExecution.Status != deliverydomain.ExecutionPartiallyApplied ||
		storedExecution.CompletedAt != nil {
		t.Fatalf(
			"stale activation = approval:%+v plan:%+v revision:%+v execution:%+v",
			storedApproval,
			storedPlan,
			storedRevision,
			storedExecution,
		)
	}
	assertDeliveryReservationStatus(
		t,
		db,
		tenantID,
		execution.ID,
		executionReservationReconciliationRequired,
	)
	events, err := store.Replay(t.Context(), deliveryservice.StreamCursor{
		TenantID:      tenantID,
		AggregateType: deliveryservice.AggregateExecution,
		AggregateID:   string(execution.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertDeliveryEvent(
		t,
		events,
		deliveryservice.EventActivationFailed,
		"system:delivery-effect-worker",
	)
	revisionEvents, err := store.Replay(t.Context(), deliveryservice.StreamCursor{
		TenantID:      tenantID,
		AggregateType: deliveryservice.AggregateRevision,
		AggregateID:   string(revision.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertDeliveryEvent(
		t,
		revisionEvents,
		deliveryservice.EventActivationFailed,
		"system:delivery-effect-worker",
	)
}

func prepareDeliveryApprovalFixture(
	t *testing.T,
	store *DeliveryStore,
	tenantID deliverydomain.TenantID,
	now time.Time,
	expiresAt time.Time,
	effectCount int,
) (
	deliverydomain.DispatchPlan,
	deliverydomain.PlanRevision,
	deliverydomain.PlanApproval,
	[]deliverydomain.EffectPreview,
) {
	t.Helper()
	plan, revision := createValidatedDeliveryRevision(t, store, tenantID, now)
	previews := make([]deliverydomain.EffectPreview, 0, effectCount)
	for index := range effectCount {
		parameters := json.RawMessage(fmt.Sprintf(
			`{"duty_index":%d,"plan_id":"%s"}`,
			index,
			plan.ID,
		))
		parametersDigest, err := deliverydomain.DigestCanonicalJSON(parameters)
		if err != nil {
			t.Fatal(err)
		}
		previews = append(previews, deliverydomain.EffectPreview{
			ID:                             deliverydomain.EffectID(fmt.Sprintf("effect-%d", index+1)),
			Ordinal:                        uint32(index),
			Action:                         deliverydomain.EffectCreateRoute,
			Target:                         fmt.Sprintf("%s/duty/%d", plan.ID, index),
			Parameters:                     parameters,
			ParametersDigest:               parametersDigest,
			Required:                       true,
			AdapterID:                      "tms-v1",
			ContractVersion:                "v1",
			KeyRetentionSeconds:            int64((48 * time.Hour) / time.Second),
			LookupConsistencyWindowSeconds: int64(time.Minute / time.Second),
		})
	}
	approval := deliverydomain.PlanApproval{
		TenantID: tenantID,
		ID:       "approval-1",
		Binding: deliverydomain.ApprovalBinding{
			TenantID:               tenantID,
			PlanID:                 plan.ID,
			RevisionID:             revision.ID,
			ActiveVersion:          plan.ActiveVersion,
			ProblemDigest:          revision.ProblemDigest,
			PolicyDigest:           revision.PolicyDigest,
			CommitmentDigest:       revision.CommitmentDigest,
			PlanDigest:             revision.PlanDigest,
			ValidationReportDigest: revision.ValidationReportDigest,
			EffectSetDigest:        deliveryDigest("d"),
		},
		EffectSetArtifact: deliveryDigest("e"),
		Status:            deliverydomain.ApprovalPending,
		Version:           1,
		RequestedBy:       "agent:delivery",
		PlanCreatedBy:     "dispatcher-1",
		Reason:            "review validated delivery plan",
		RequestedAt:       now.Add(time.Minute),
		ExpiresAt:         expiresAt,
	}
	stored, _, err := store.PrepareApproval(
		t.Context(),
		deliveryservice.PrepareApprovalTx{
			IdempotencyKey:          "approval-key",
			RequestDigest:           deliveryDigest("f"),
			ExpectedRevisionVersion: revision.Version,
			Actor:                   deliveryservice.Actor{Subject: approval.RequestedBy},
			Approval:                approval,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return plan, revision, stored, previews
}

func activateCompetingDeliveryRevision(
	t *testing.T,
	db *DB,
	tenantID deliverydomain.TenantID,
	plan deliverydomain.DispatchPlan,
	sourceRevision deliverydomain.PlanRevision,
	now time.Time,
) {
	t.Helper()
	tx, err := db.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err := tx.Exec(t.Context(), `
		INSERT INTO waybill.delivery_runs (
			tenant_id, run_id, problem_id, problem_version, problem_digest,
			solver_profile, config_digest, status, version, fencing_token,
			created_at, updated_at, closed_at
		) VALUES (
			$1, 'run-competing', $2, 1, $3,
			'solver-v1', $4, 'succeeded', 1, 0,
			$5, $5, $5
		)
	`, tenantID, plan.ProblemID, sourceRevision.ProblemDigest,
		deliveryDigest("8"), now); err != nil {
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
			$1, 'revision-competing', $2, 'run-competing',
			$3, $4, $5,
			$6, $7,
			$8, $9,
			'active', 1, $10, $10
		)
	`, tenantID, plan.ID, sourceRevision.ProblemDigest,
		sourceRevision.PolicyDigest, sourceRevision.CommitmentDigest,
		deliveryDigest("4"), deliveryDigest("5"),
		deliveryDigest("6"), deliveryDigest("7"), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `
		UPDATE waybill.delivery_runs
		SET result_revision_id = 'revision-competing'
		WHERE tenant_id = $1 AND run_id = 'run-competing'
	`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `
		UPDATE waybill.delivery_plans
		SET active_revision_id = 'revision-competing',
		    active_version = 1,
		    updated_at = $3
		WHERE tenant_id = $1 AND plan_id = $2
	`, tenantID, plan.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func confirmDeliveryExecutionFixture(
	t *testing.T,
	store *DeliveryStore,
	approval deliverydomain.PlanApproval,
	previews []deliverydomain.EffectPreview,
	now time.Time,
) deliverydomain.DispatchExecution {
	t.Helper()
	execution := deliverydomain.DispatchExecution{
		TenantID:        approval.TenantID,
		ID:              "execution-1",
		ApprovalID:      approval.ID,
		PlanID:          approval.Binding.PlanID,
		RevisionID:      approval.Binding.RevisionID,
		EffectSetDigest: approval.Binding.EffectSetDigest,
		Status:          deliverydomain.ExecutionPrepared,
		Version:         1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	effects := make([]deliverydomain.EffectRecord, 0, len(previews))
	adapter := &scriptedDeliveryAdapter{}
	for _, preview := range previews {
		key := "delivery-effect-v1:" + string(preview.ID)
		binding, err := deliveryexecution.NewBinding(
			preview,
			key,
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
	stored, _, err := store.DecideAndCreateExecution(
		t.Context(),
		deliveryservice.DecideExecutionTx{
			TenantID:                 approval.TenantID,
			IdempotencyKey:           "confirm-approval",
			RequestDigest:            deliveryDigest("0"),
			Actor:                    deliveryservice.Actor{Subject: "supervisor-1"},
			ApprovalID:               approval.ID,
			ExpectedVersion:          approval.Version,
			ExpectedActiveRevisionID: approval.Binding.BaseRevisionID,
			ExpectedActiveVersion:    approval.Binding.ActiveVersion,
			Decision:                 deliveryservice.ConfirmApproval,
			Execution:                execution,
			Effects:                  effects,
			Now:                      now,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func claimOneDeliveryEffect(
	t *testing.T,
	store *DeliveryStore,
	tenantID deliverydomain.TenantID,
	workerID string,
	now time.Time,
) deliveryservice.EffectClaim {
	t.Helper()
	claims, err := store.ClaimEffects(t.Context(), deliveryservice.ClaimEffects{
		TenantID: tenantID,
		WorkerID: workerID,
		Limit:    1,
		LeaseTTL: time.Minute,
		Now:      now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 {
		t.Fatalf("claimed effects = %d, want 1", len(claims))
	}
	return claims[0]
}

func assertDeliveryApprovalState(
	t *testing.T,
	store *DeliveryStore,
	tenantID deliverydomain.TenantID,
	approvalID deliverydomain.ApprovalID,
	revisionID deliverydomain.PlanRevisionID,
	approvalStatus deliverydomain.ApprovalStatus,
	revisionStatus deliverydomain.RevisionStatus,
) {
	t.Helper()
	approval, err := store.GetApproval(t.Context(), tenantID, approvalID)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := store.GetRevision(t.Context(), tenantID, revisionID)
	if err != nil {
		t.Fatal(err)
	}
	if approval.Status != approvalStatus || revision.Status != revisionStatus {
		t.Fatalf("approval = %+v, revision = %+v", approval, revision)
	}
	events, err := store.Replay(t.Context(), deliveryservice.StreamCursor{
		TenantID:      tenantID,
		AggregateType: deliveryservice.AggregateRevision,
		AggregateID:   string(revisionID),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertDeliveryEvent(
		t,
		events,
		deliveryservice.EventApprovalDecided,
		"system:delivery-recovery",
	)
}
