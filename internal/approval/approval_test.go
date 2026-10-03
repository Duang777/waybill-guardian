package approval

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
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
