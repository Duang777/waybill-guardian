package approval

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/idempotency"
)

func TestApprovalStateMachineAndRecovery(t *testing.T) {
	now := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	journal, err := audit.Open(dir, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(journal, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(context.Background(), testApproval("run-1", "call-1"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != StatusPending {
		t.Fatalf("created status = %q", created.Status)
	}

	confirmed, err := store.Decide(context.Background(), created.ID, Decision{
		Kind:      DecisionConfirm,
		DecidedBy: "reviewer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Status != StatusConfirmed {
		t.Fatalf("confirmed status = %q", confirmed.Status)
	}
	repeated, err := store.Decide(context.Background(), created.ID, Decision{
		Kind:      DecisionConfirm,
		DecidedBy: "reviewer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Status != StatusConfirmed {
		t.Fatalf("repeated status = %q", repeated.Status)
	}
	if _, err := store.Decide(context.Background(), created.ID, Decision{
		Kind:         DecisionReject,
		DecidedBy:    "reviewer",
		RejectReason: "unsafe",
	}); !errors.Is(err, ErrDecisionConflict) {
		t.Fatalf("reject after confirm error = %v", err)
	}
	executed, err := store.MarkExecuted(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if executed.Status != StatusExecuted {
		t.Fatalf("executed status = %q", executed.Status)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedJournal, err := audit.Open(dir, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedJournal.Close()
	reopened, err := NewStore(reopenedJournal, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := reopened.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != StatusExecuted || recovered.DecidedBy != "reviewer" {
		t.Fatalf("recovered approval = %+v", recovered)
	}
}

func TestRejectRequiresReasonAndExpiryDefaultsToReject(t *testing.T) {
	now := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)
	journal, err := audit.Open(t.TempDir(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	store, err := NewStore(journal, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Create(context.Background(), testApproval("run-2", "call-2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(context.Background(), first.ID, Decision{
		Kind: DecisionReject,
	}); !errors.Is(err, ErrRejectReason) {
		t.Fatalf("empty reject reason error = %v", err)
	}

	second := testApproval("run-3", "call-3")
	second.ExpiresAt = now.Add(time.Minute)
	second, err = store.Create(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	expired, err := store.ExpireDue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].ID != second.ID || expired[0].Status != StatusExpired {
		t.Fatalf("expired approvals = %+v", expired)
	}
}

func TestConfirmHonorsExpiryBoundary(t *testing.T) {
	now := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)
	journal, err := audit.Open(t.TempDir(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	store, err := NewStore(journal, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	deadline := now.Add(time.Minute)
	beforeDeadline := testApproval("run-before-deadline", "call-before-deadline")
	beforeDeadline.ExpiresAt = deadline
	beforeDeadline, err = store.Create(context.Background(), beforeDeadline)
	if err != nil {
		t.Fatal(err)
	}
	now = deadline.Add(-time.Nanosecond)
	confirmed, err := store.Decide(context.Background(), beforeDeadline.ID, Decision{
		Kind:      DecisionConfirm,
		DecidedBy: "reviewer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Status != StatusConfirmed {
		t.Fatalf("status before deadline = %q", confirmed.Status)
	}

	atDeadline := testApproval("run-at-deadline", "call-at-deadline")
	atDeadline.ExpiresAt = deadline
	atDeadline, err = store.Create(context.Background(), atDeadline)
	if err != nil {
		t.Fatal(err)
	}
	now = deadline
	if _, err := store.Decide(context.Background(), atDeadline.ID, Decision{
		Kind:      DecisionConfirm,
		DecidedBy: "reviewer",
	}); !errors.Is(err, ErrDecisionConflict) {
		t.Fatalf("confirm at deadline error = %v, want ErrDecisionConflict", err)
	}
	expired, err := store.Get(atDeadline.ID)
	if err != nil {
		t.Fatal(err)
	}
	if expired.Status != StatusExpired {
		t.Fatalf("status at deadline = %q, want expired", expired.Status)
	}
	if expired.DecidedBy != "system" || expired.DecidedAt == nil || !expired.DecidedAt.Equal(deadline) {
		t.Fatalf("expiry decision = %+v", expired)
	}
}

func TestRebuildsPartiallyFailedApproval(t *testing.T) {
	now := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	journal, err := audit.Open(dir, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(journal, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	value := testApproval("run-partial", "call-reassign")
	value.ID = IDFor(value.RunID, []string{"call-reassign", "call-sms"})
	value.Items = append(value.Items, Item{
		CallID:         "call-sms",
		Action:         domain.ActionSendSMS,
		WireName:       "notify_send_sms",
		Params:         json.RawMessage(`{"idempotency_key":"key-sms"}`),
		ArgumentsHash:  "hash-sms",
		IdempotencyKey: "key-sms",
	})
	created, err := store.Create(context.Background(), value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(context.Background(), created.ID, Decision{
		Kind:      DecisionConfirm,
		DecidedBy: "reviewer",
	}); err != nil {
		t.Fatal(err)
	}
	failed, err := store.MarkExecutionFailed(context.Background(), created.ID, []ItemExecution{
		{
			CallID:         "call-reassign",
			Action:         domain.ActionReassign,
			IdempotencyKey: "key",
			Status:         ExecutionSucceeded,
		},
		{
			CallID:         "call-sms",
			Action:         domain.ActionSendSMS,
			IdempotencyKey: "key-sms",
			Status:         ExecutionFailed,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != StatusPartiallyFailed {
		t.Fatalf("failed status = %q, want partially_failed", failed.Status)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedJournal, err := audit.Open(dir, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedJournal.Close()
	reopened, err := NewStore(reopenedJournal, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := reopened.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != StatusPartiallyFailed {
		t.Fatalf("recovered status = %q, want partially_failed", recovered.Status)
	}
}

func TestCreateRejectsDuplicateCurrentIdentity(t *testing.T) {
	journal, err := audit.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	store, err := NewStore(journal, time.Now)
	if err != nil {
		t.Fatal(err)
	}

	value := testCurrentApproval(t, "run-duplicate", "call-1")
	duplicateCall := value
	duplicateCall.Items = append(duplicateCall.Items, duplicateCall.Items[0])
	if _, err := store.Create(context.Background(), duplicateCall); !errors.Is(err, ErrDuplicateCallID) {
		t.Fatalf("duplicate call error = %v", err)
	}

	duplicateEffect := value
	second := duplicateEffect.Items[0]
	second.CallID = "call-2"
	duplicateEffect.Items = append(duplicateEffect.Items, second)
	if _, err := store.Create(context.Background(), duplicateEffect); !errors.Is(err, ErrDuplicateEffect) {
		t.Fatalf("duplicate effect error = %v", err)
	}

	partialIdentity := value
	partialIdentity.Items[0].EffectID = ""
	if _, err := store.Create(context.Background(), partialIdentity); !errors.Is(err, ErrInvalidEffectIdentity) {
		t.Fatalf("partial identity error = %v", err)
	}
}

func TestRebuildsEffectV0Approval(t *testing.T) {
	dir := t.TempDir()
	journal, err := audit.Open(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(journal, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	runContext := domain.RunContext{
		RunID:       "run-effect-v0-rebuild",
		IncidentID:  "incident-effect-v0-rebuild",
		WaybillID:   "YD2026101001",
		PlanVersion: 1,
	}
	identity, err := idempotency.EffectV0Identity(
		runContext,
		domain.ActionReassign,
		"effect-v0-business-hash",
	)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(context.Background(), Approval{
		ID:          IDFor(runContext.RunID, []string{"call-effect-v0-rebuild"}),
		RunID:       runContext.RunID,
		WaybillID:   runContext.WaybillID,
		PlanVersion: runContext.PlanVersion,
		Items: []Item{{
			CallID:         "call-effect-v0-rebuild",
			Action:         domain.ActionReassign,
			WireName:       "tms_reassign",
			Params:         json.RawMessage(`{"waybill_id":"YD2026101001","carrier_id":"CARRIER-SW-42"}`),
			ArgumentsHash:  identity.ArgumentsHash,
			EffectID:       identity.EffectID,
			IdempotencyKey: identity.Key,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedJournal, err := audit.Open(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedJournal.Close()
	reopened, err := NewStore(reopenedJournal, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := reopened.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	recoveredIdentity, err := recovered.Items[0].Identity()
	if err != nil {
		t.Fatal(err)
	}
	if recoveredIdentity != identity {
		t.Fatalf("recovered identity = %+v, want %+v", recoveredIdentity, identity)
	}
}

func TestAuthorizeUsesBusinessHashForCurrentAndFullHashForLegacy(t *testing.T) {
	journal, err := audit.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	store, err := NewStore(journal, time.Now)
	if err != nil {
		t.Fatal(err)
	}

	current := testCurrentApproval(t, "run-current", "call-current")
	current, err = store.Create(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(context.Background(), current.ID, Decision{
		Kind: DecisionConfirm,
	}); err != nil {
		t.Fatal(err)
	}
	item := current.Items[0]
	authorization, err := store.Authorize(AuthorizationRequest{
		RunID:                 current.RunID,
		CallID:                item.CallID,
		Action:                item.Action,
		WireName:              item.WireName,
		BusinessArgumentsHash: item.ArgumentsHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if authorization.Item.EffectID != item.EffectID {
		t.Fatalf("authorized effect = %q, want %q", authorization.Item.EffectID, item.EffectID)
	}
	if _, err := store.Authorize(AuthorizationRequest{
		RunID:                 current.RunID,
		CallID:                item.CallID,
		Action:                item.Action,
		WireName:              item.WireName,
		BusinessArgumentsHash: "changed",
	}); !errors.Is(err, ErrApprovalNotGranted) {
		t.Fatalf("changed current arguments error = %v", err)
	}

	legacy := testApproval("run-legacy", "call-legacy")
	legacy.Items = append(legacy.Items, Item{
		CallID:         "call-legacy-2",
		Action:         domain.ActionSendSMS,
		WireName:       "notify_send_sms",
		Params:         json.RawMessage(`{"idempotency_key":"key"}`),
		ArgumentsHash:  "other-full-hash",
		IdempotencyKey: "key",
	})
	legacy.ID = IDFor(legacy.RunID, []string{"call-legacy", "call-legacy-2"})
	legacy, err = store.Create(context.Background(), legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(context.Background(), legacy.ID, Decision{
		Kind: DecisionConfirm,
	}); err != nil {
		t.Fatal(err)
	}
	legacyAuthorization, err := store.Authorize(AuthorizationRequest{
		RunID:               legacy.RunID,
		CallID:              "call-legacy",
		Action:              domain.ActionReassign,
		WireName:            "tms_reassign",
		LegacyArgumentsHash: "hash",
		LegacyKey:           "key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !legacyAuthorization.LegacyAmbiguous {
		t.Fatal("shared legacy key was not marked ambiguous")
	}

	v0RunContext := domain.RunContext{
		RunID:       "run-effect-v0",
		IncidentID:  "incident-effect-v0",
		WaybillID:   "YD2026101001",
		PlanVersion: 1,
	}
	v0Identity, err := idempotency.EffectV0Identity(
		v0RunContext,
		domain.ActionReassign,
		"v0-business-hash",
	)
	if err != nil {
		t.Fatal(err)
	}
	v0 := Approval{
		ID:          IDFor(v0RunContext.RunID, []string{"call-effect-v0"}),
		RunID:       v0RunContext.RunID,
		WaybillID:   v0RunContext.WaybillID,
		PlanVersion: v0RunContext.PlanVersion,
		Items: []Item{{
			CallID:         "call-effect-v0",
			Action:         domain.ActionReassign,
			WireName:       "tms_reassign",
			Params:         json.RawMessage(`{"waybill_id":"YD2026101001","carrier_id":"CARRIER-SW-42"}`),
			ArgumentsHash:  v0Identity.ArgumentsHash,
			EffectID:       v0Identity.EffectID,
			IdempotencyKey: v0Identity.Key,
		}},
	}
	v0, err = store.Create(context.Background(), v0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(context.Background(), v0.ID, Decision{
		Kind: DecisionConfirm,
	}); err != nil {
		t.Fatal(err)
	}
	v0Authorization, err := store.Authorize(AuthorizationRequest{
		RunID:                 v0.RunID,
		CallID:                "call-effect-v0",
		Action:                domain.ActionReassign,
		WireName:              "tms_reassign",
		BusinessArgumentsHash: v0Identity.ArgumentsHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := v0Authorization.Item.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if normalized != v0Identity {
		t.Fatalf("v0 identity = %+v, want %+v", normalized, v0Identity)
	}
}

func testApproval(runID, callID string) Approval {
	params, _ := json.Marshal(map[string]string{
		"waybill_id":      "YD2026101001",
		"carrier_id":      "CARRIER-SW-42",
		"idempotency_key": "key",
	})
	return Approval{
		ID:        IDFor(domain.RunID(runID), []string{callID}),
		RunID:     domain.RunID(runID),
		WaybillID: "YD2026101001",
		Items: []Item{{
			CallID:         callID,
			Action:         domain.ActionReassign,
			WireName:       "tms_reassign",
			Params:         params,
			ArgumentsHash:  "hash",
			IdempotencyKey: "key",
		}},
		Reason: "fatigue and extended stop",
		Evidence: []Evidence{
			{Label: "continuous_drive_hours", Value: "9"},
		},
	}
}

func testCurrentApproval(t *testing.T, runID, callID string) Approval {
	t.Helper()
	params := json.RawMessage(`{"waybill_id":"YD2026101001","carrier_id":"CARRIER-SW-42"}`)
	identity, err := idempotency.Derive(idempotency.DerivationInput{
		RunContext: domain.RunContext{
			RunID:       domain.RunID(runID),
			IncidentID:  "incident-current",
			WaybillID:   "YD2026101001",
			PlanVersion: 1,
		},
		Action:    domain.ActionReassign,
		Target:    "waybill/YD2026101001/carrier/CARRIER-SW-42",
		Arguments: params,
	})
	if err != nil {
		t.Fatal(err)
	}
	return Approval{
		ID:          IDFor(domain.RunID(runID), []string{callID}),
		RunID:       domain.RunID(runID),
		WaybillID:   "YD2026101001",
		PlanVersion: 1,
		Items: []Item{{
			CallID:          callID,
			Action:          domain.ActionReassign,
			WireName:        "tms_reassign",
			Params:          params,
			ArgumentsHash:   identity.ArgumentsHash,
			IdentityVersion: identity.Version,
			EffectID:        identity.EffectID,
			IdempotencyKey:  identity.Key,
		}},
	}
}
