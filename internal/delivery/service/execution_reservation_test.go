package service

import (
	"errors"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func TestExecutionReservationBlocksConcurrentCandidateBeforeEffects(t *testing.T) {
	now := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	current := ActivePlanTuple{RevisionID: "revision-base", Version: 7}
	firstBinding := reservationBinding("execution-a", "revision-a", current)
	first, err := ReserveExecution(ReserveExecutionInput{
		Current: current,
		Binding: firstBinding,
		Now:     now,
	})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := ReserveExecution(ReserveExecutionInput{
		Current:  current,
		Existing: &first.Reservation,
		Binding:  firstBinding,
		Now:      now.Add(time.Second),
	})
	if err != nil || !replay.Replayed {
		t.Fatalf("reservation replay = %+v, err = %v", replay, err)
	}

	secondBinding := reservationBinding("execution-b", "revision-b", current)
	_, err = ReserveExecution(ReserveExecutionInput{
		Current:  current,
		Existing: &first.Reservation,
		Binding:  secondBinding,
		Now:      now.Add(time.Second),
	})
	if !errors.Is(err, ErrExecutionReservationHeld) {
		t.Fatalf("competing reservation error = %v", err)
	}
	if err := AuthorizeReservedEffect(ReservationEffectClaimInput{
		Current:     current,
		Reservation: first.Reservation,
		ExecutionID: secondBinding.ExecutionID,
		Operation:   ReservationEffectDispatch,
		Required:    true,
	}); !errors.Is(err, ErrExecutionReservationHeld) {
		t.Fatalf("loser effect claim error = %v", err)
	}
}

func TestExecutionReservationRetainsOwnershipDuringReconciliation(t *testing.T) {
	now := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	current := ActivePlanTuple{RevisionID: "revision-base", Version: 7}
	reserved, err := ReserveExecution(ReserveExecutionInput{
		Current: current,
		Binding: reservationBinding("execution-a", "revision-a", current),
		Now:     now,
	})
	if err != nil {
		t.Fatal(err)
	}
	reconciliation, err := RequireReservationReconciliation(
		reserved.Reservation,
		true,
		now.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := AuthorizeReservedEffect(ReservationEffectClaimInput{
		Current:     current,
		Reservation: reconciliation,
		ExecutionID: reconciliation.Binding.ExecutionID,
		Operation:   ReservationEffectDispatch,
		Required:    true,
	}); !errors.Is(err, ErrExecutionReservationHeld) {
		t.Fatalf("dispatch during reconciliation error = %v", err)
	}
	if err := AuthorizeReservedEffect(ReservationEffectClaimInput{
		Current:     current,
		Reservation: reconciliation,
		ExecutionID: reconciliation.Binding.ExecutionID,
		Operation:   ReservationEffectLookup,
		Required:    true,
	}); err != nil {
		t.Fatalf("lookup during reconciliation was rejected: %v", err)
	}
	resolved, err := ResolveReservationReconciliation(
		reconciliation,
		true,
		now.Add(90*time.Second),
	)
	if err != nil || resolved.State != ExecutionReservationReserved {
		t.Fatalf("resolved reconciliation = %+v, err = %v", resolved, err)
	}
	_, err = ReserveExecution(ReserveExecutionInput{
		Current:  current,
		Existing: &resolved,
		Binding:  reservationBinding("execution-b", "revision-b", current),
		Now:      now.Add(2 * time.Minute),
	})
	if !errors.Is(err, ErrExecutionReservationHeld) {
		t.Fatalf("reconciliation reservation was displaced: %v", err)
	}
	if _, err := ReleaseExecutionReservation(
		reconciliation,
		false,
		false,
		now.Add(3*time.Minute),
	); err == nil {
		t.Fatal("uncompensated reconciliation reservation was released")
	}
	if _, err := ReleaseExecutionReservation(
		reconciliation,
		true,
		false,
		now.Add(3*time.Minute),
	); err == nil {
		t.Fatal("reconciliation reservation trusted a contradictory no-effect claim")
	}
}

func TestActivateReservedRevisionCommitsTupleAndRejectsOldBinding(t *testing.T) {
	now := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	current := ActivePlanTuple{RevisionID: "revision-base", Version: 7}
	reserved, err := ReserveExecution(ReserveExecutionInput{
		Current: current,
		Binding: reservationBinding("execution-a", "revision-a", current),
		Now:     now,
	})
	if err != nil {
		t.Fatal(err)
	}
	activated, err := ActivateReservedRevision(ActivateReservationInput{
		Current:                  current,
		Reservation:              reserved.Reservation,
		RequiredEffectsSucceeded: true,
		ExternalEffectObserved:   true,
		Now:                      now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !activated.Activated ||
		activated.Active != (ActivePlanTuple{
			RevisionID: "revision-a",
			Version:    8,
		}) ||
		activated.Reservation.State != ExecutionReservationCommitted {
		t.Fatalf("activation outcome = %+v", activated)
	}
	_, err = ReserveExecution(ReserveExecutionInput{
		Current: activated.Active,
		Binding: reservationBinding("execution-b", "revision-b", current),
		Now:     now.Add(2 * time.Minute),
	})
	if !errors.Is(err, ErrExecutionReservationStale) {
		t.Fatalf("old active tuple reservation error = %v", err)
	}
	if err := AuthorizeReservedEffect(ReservationEffectClaimInput{
		Current:     activated.Active,
		Reservation: activated.Reservation,
		ExecutionID: activated.Reservation.Binding.ExecutionID,
		Operation:   ReservationEffectDispatch,
		Required:    false,
	}); err != nil {
		t.Fatalf("optional committed effect was rejected: %v", err)
	}
	replay, err := ReserveExecution(ReserveExecutionInput{
		Current:  activated.Active,
		Existing: &activated.Reservation,
		Binding:  activated.Reservation.Binding,
		Now:      now.Add(3 * time.Minute),
	})
	if err != nil || !replay.Replayed ||
		replay.Reservation.State != ExecutionReservationCommitted {
		t.Fatalf("committed reservation replay = %+v, err = %v", replay, err)
	}
}

func TestActivationLossAfterExternalEffectRequiresReconciliation(t *testing.T) {
	now := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	base := ActivePlanTuple{RevisionID: "revision-base", Version: 7}
	reserved, err := ReserveExecution(ReserveExecutionInput{
		Current: base,
		Binding: reservationBinding("execution-a", "revision-a", base),
		Now:     now,
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := ActivateReservedRevision(ActivateReservationInput{
		Current: ActivePlanTuple{
			RevisionID: "revision-other",
			Version:    8,
		},
		Reservation:              reserved.Reservation,
		RequiredEffectsSucceeded: true,
		ExternalEffectObserved:   true,
		Now:                      now.Add(time.Minute),
	})
	if !errors.Is(err, ErrExecutionReservationStale) ||
		outcome.Reservation.State != ExecutionReservationReconciliationRequired ||
		outcome.Activated {
		t.Fatalf("stale activation outcome = %+v, err = %v", outcome, err)
	}
}

func TestExecutionReservationRejectsIncompleteBindingAndTimeRegression(t *testing.T) {
	now := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	base := ActivePlanTuple{RevisionID: "revision-base", Version: 7}
	binding := reservationBinding("execution-a", "revision-a", base)

	for _, mutate := range []func(*ExecutionReservationBinding){
		func(value *ExecutionReservationBinding) { value.BaseRevisionID = "" },
		func(value *ExecutionReservationBinding) { value.BaseActiveVersion = 0 },
		func(value *ExecutionReservationBinding) { value.BaseActiveVersion = ^uint64(0) },
	} {
		invalid := binding
		mutate(&invalid)
		if _, err := ReserveExecution(ReserveExecutionInput{
			Current: base,
			Binding: invalid,
			Now:     now,
		}); err == nil {
			t.Fatalf("invalid reservation binding was accepted: %+v", invalid)
		}
	}

	reserved, err := ReserveExecution(ReserveExecutionInput{
		Current: base,
		Binding: binding,
		Now:     now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RequireReservationReconciliation(
		reserved.Reservation,
		true,
		now.Add(-time.Second),
	); err == nil {
		t.Fatal("reservation timestamp regression was accepted")
	}
	if _, err := ActivateReservedRevision(ActivateReservationInput{
		Current:                  base,
		Reservation:              reserved.Reservation,
		RequiredEffectsSucceeded: true,
		Now:                      now.Add(-time.Second),
	}); err == nil {
		t.Fatal("activation timestamp regression was accepted")
	}
}

func reservationBinding(
	executionID domain.ExecutionID,
	candidateRevisionID domain.PlanRevisionID,
	base ActivePlanTuple,
) ExecutionReservationBinding {
	return ExecutionReservationBinding{
		TenantID:            "tenant-1",
		PlanID:              "plan-1",
		ExecutionID:         executionID,
		CandidateRevisionID: candidateRevisionID,
		BaseRevisionID:      base.RevisionID,
		BaseActiveVersion:   base.Version,
	}
}
