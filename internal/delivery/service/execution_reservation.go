package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

var (
	ErrExecutionReservationHeld  = errors.New("delivery execution reservation is held")
	ErrExecutionReservationStale = errors.New("delivery execution reservation binding is stale")
)

type ExecutionReservationState string

const (
	ExecutionReservationReserved               ExecutionReservationState = "reserved"
	ExecutionReservationCommitted              ExecutionReservationState = "committed"
	ExecutionReservationReconciliationRequired ExecutionReservationState = "reconciliation_required"
	ExecutionReservationReleased               ExecutionReservationState = "released"
)

type ActivePlanTuple struct {
	RevisionID domain.PlanRevisionID `json:"revision_id"`
	Version    uint64                `json:"version"`
}

type ExecutionReservationBinding struct {
	TenantID            domain.TenantID       `json:"tenant_id"`
	PlanID              domain.PlanID         `json:"plan_id"`
	ExecutionID         domain.ExecutionID    `json:"execution_id"`
	CandidateRevisionID domain.PlanRevisionID `json:"candidate_revision_id"`
	BaseRevisionID      domain.PlanRevisionID `json:"base_revision_id"`
	BaseActiveVersion   uint64                `json:"base_active_version"`
}

type ExecutionReservation struct {
	Binding   ExecutionReservationBinding `json:"binding"`
	State     ExecutionReservationState   `json:"state"`
	Version   uint64                      `json:"version"`
	CreatedAt time.Time                   `json:"created_at"`
	UpdatedAt time.Time                   `json:"updated_at"`
}

type ReserveExecutionInput struct {
	Current  ActivePlanTuple
	Existing *ExecutionReservation
	Binding  ExecutionReservationBinding
	Now      time.Time
}

type ReserveExecutionOutput struct {
	Reservation ExecutionReservation
	Replayed    bool
}

type ReservationEffectOperation string

const (
	ReservationEffectDispatch ReservationEffectOperation = "dispatch"
	ReservationEffectLookup   ReservationEffectOperation = "lookup"
)

type ReservationEffectClaimInput struct {
	Current     ActivePlanTuple
	Reservation ExecutionReservation
	ExecutionID domain.ExecutionID
	Operation   ReservationEffectOperation
	Required    bool
}

type ActivateReservationInput struct {
	Current                  ActivePlanTuple
	Reservation              ExecutionReservation
	RequiredEffectsSucceeded bool
	ExternalEffectObserved   bool
	Now                      time.Time
}

type ActivateReservationOutput struct {
	Active      ActivePlanTuple
	Reservation ExecutionReservation
	Activated   bool
}

func ReserveExecution(
	input ReserveExecutionInput,
) (ReserveExecutionOutput, error) {
	if err := validateReservationBinding(input.Binding); err != nil {
		return ReserveExecutionOutput{}, err
	}
	if input.Now.IsZero() {
		return ReserveExecutionOutput{}, fmt.Errorf("reservation time is required")
	}
	if input.Existing != nil && input.Existing.Binding == input.Binding {
		if err := validateReservation(*input.Existing); err != nil {
			return ReserveExecutionOutput{}, err
		}
		return ReserveExecutionOutput{
			Reservation: *input.Existing,
			Replayed:    true,
		}, nil
	}
	expected := ActivePlanTuple{
		RevisionID: input.Binding.BaseRevisionID,
		Version:    input.Binding.BaseActiveVersion,
	}
	if input.Current != expected {
		return ReserveExecutionOutput{}, ErrExecutionReservationStale
	}
	if input.Existing != nil {
		existing := *input.Existing
		if existing.State != ExecutionReservationReleased {
			return ReserveExecutionOutput{}, ErrExecutionReservationHeld
		}
	}
	now := input.Now.UTC()
	return ReserveExecutionOutput{
		Reservation: ExecutionReservation{
			Binding:   input.Binding,
			State:     ExecutionReservationReserved,
			Version:   1,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}, nil
}

func AuthorizeReservedEffect(input ReservationEffectClaimInput) error {
	if err := validateReservation(input.Reservation); err != nil {
		return err
	}
	if input.ExecutionID != input.Reservation.Binding.ExecutionID {
		return ErrExecutionReservationHeld
	}
	switch input.Operation {
	case ReservationEffectDispatch, ReservationEffectLookup:
	default:
		return fmt.Errorf("unsupported reservation effect operation %q", input.Operation)
	}
	binding := input.Reservation.Binding
	switch input.Reservation.State {
	case ExecutionReservationReserved:
		if input.Current != (ActivePlanTuple{
			RevisionID: binding.BaseRevisionID,
			Version:    binding.BaseActiveVersion,
		}) {
			return ErrExecutionReservationStale
		}
		return nil
	case ExecutionReservationReconciliationRequired:
		if input.Operation != ReservationEffectLookup {
			return ErrExecutionReservationHeld
		}
		return nil
	case ExecutionReservationCommitted:
		if input.Required ||
			input.Current != (ActivePlanTuple{
				RevisionID: binding.CandidateRevisionID,
				Version:    binding.BaseActiveVersion + 1,
			}) {
			return ErrExecutionReservationStale
		}
		return nil
	default:
		return ErrExecutionReservationHeld
	}
}

func RequireReservationReconciliation(
	reservation ExecutionReservation,
	externalEffectObserved bool,
	now time.Time,
) (ExecutionReservation, error) {
	if err := validateReservation(reservation); err != nil {
		return ExecutionReservation{}, err
	}
	if reservation.State == ExecutionReservationReconciliationRequired {
		return reservation, nil
	}
	transitionAt, err := reservationTransitionTime(reservation, now)
	if err != nil {
		return ExecutionReservation{}, err
	}
	if reservation.State != ExecutionReservationReserved ||
		!externalEffectObserved {
		return ExecutionReservation{}, fmt.Errorf(
			"reservation cannot enter reconciliation_required",
		)
	}
	reservation.State = ExecutionReservationReconciliationRequired
	reservation.Version++
	reservation.UpdatedAt = transitionAt
	return reservation, nil
}

func ResolveReservationReconciliation(
	reservation ExecutionReservation,
	requiredEffectsVerified bool,
	now time.Time,
) (ExecutionReservation, error) {
	if err := validateReservation(reservation); err != nil {
		return ExecutionReservation{}, err
	}
	transitionAt, err := reservationTransitionTime(reservation, now)
	if err != nil {
		return ExecutionReservation{}, err
	}
	if reservation.State != ExecutionReservationReconciliationRequired ||
		!requiredEffectsVerified {
		return ExecutionReservation{}, fmt.Errorf(
			"reservation reconciliation is not resolved",
		)
	}
	reservation.State = ExecutionReservationReserved
	reservation.Version++
	reservation.UpdatedAt = transitionAt
	return reservation, nil
}

func ActivateReservedRevision(
	input ActivateReservationInput,
) (ActivateReservationOutput, error) {
	if err := validateReservation(input.Reservation); err != nil {
		return ActivateReservationOutput{}, err
	}
	if input.Now.IsZero() {
		return ActivateReservationOutput{}, fmt.Errorf("activation time is required")
	}
	reservation := input.Reservation
	if reservation.State == ExecutionReservationCommitted {
		active := ActivePlanTuple{
			RevisionID: reservation.Binding.CandidateRevisionID,
			Version:    reservation.Binding.BaseActiveVersion + 1,
		}
		if input.Current != active {
			return ActivateReservationOutput{}, ErrExecutionReservationStale
		}
		return ActivateReservationOutput{
			Active:      active,
			Reservation: reservation,
			Activated:   true,
		}, nil
	}
	transitionAt, err := reservationTransitionTime(reservation, input.Now)
	if err != nil {
		return ActivateReservationOutput{}, err
	}
	if reservation.State != ExecutionReservationReserved ||
		!input.RequiredEffectsSucceeded {
		return ActivateReservationOutput{}, fmt.Errorf(
			"reservation is not ready for activation",
		)
	}
	expected := ActivePlanTuple{
		RevisionID: reservation.Binding.BaseRevisionID,
		Version:    reservation.Binding.BaseActiveVersion,
	}
	if input.Current != expected {
		if input.ExternalEffectObserved {
			reconciled, err := RequireReservationReconciliation(
				reservation,
				true,
				input.Now,
			)
			if err != nil {
				return ActivateReservationOutput{}, err
			}
			return ActivateReservationOutput{
				Active:      input.Current,
				Reservation: reconciled,
			}, ErrExecutionReservationStale
		}
		return ActivateReservationOutput{}, ErrExecutionReservationStale
	}
	reservation.State = ExecutionReservationCommitted
	reservation.Version++
	reservation.UpdatedAt = transitionAt
	active := ActivePlanTuple{
		RevisionID: reservation.Binding.CandidateRevisionID,
		Version:    reservation.Binding.BaseActiveVersion + 1,
	}
	return ActivateReservationOutput{
		Active:      active,
		Reservation: reservation,
		Activated:   true,
	}, nil
}

func ReleaseExecutionReservation(
	reservation ExecutionReservation,
	noExternalEffectStarted bool,
	compensationVerified bool,
	now time.Time,
) (ExecutionReservation, error) {
	if err := validateReservation(reservation); err != nil {
		return ExecutionReservation{}, err
	}
	if reservation.State == ExecutionReservationReleased {
		return reservation, nil
	}
	transitionAt, err := reservationTransitionTime(reservation, now)
	if err != nil {
		return ExecutionReservation{}, err
	}
	if reservation.State == ExecutionReservationCommitted ||
		(reservation.State == ExecutionReservationReconciliationRequired &&
			!compensationVerified) ||
		(reservation.State == ExecutionReservationReserved &&
			!noExternalEffectStarted &&
			!compensationVerified) {
		return ExecutionReservation{}, fmt.Errorf(
			"execution reservation cannot be released",
		)
	}
	reservation.State = ExecutionReservationReleased
	reservation.Version++
	reservation.UpdatedAt = transitionAt
	return reservation, nil
}

func validateReservation(value ExecutionReservation) error {
	if err := validateReservationBinding(value.Binding); err != nil {
		return err
	}
	if value.Version == 0 ||
		value.CreatedAt.IsZero() ||
		value.UpdatedAt.IsZero() ||
		value.UpdatedAt.Before(value.CreatedAt) {
		return fmt.Errorf("execution reservation version and timestamps are invalid")
	}
	switch value.State {
	case ExecutionReservationReserved,
		ExecutionReservationCommitted,
		ExecutionReservationReconciliationRequired,
		ExecutionReservationReleased:
		return nil
	default:
		return fmt.Errorf("execution reservation state %q is invalid", value.State)
	}
}

func validateReservationBinding(value ExecutionReservationBinding) error {
	if strings.TrimSpace(string(value.TenantID)) == "" ||
		strings.TrimSpace(string(value.PlanID)) == "" ||
		strings.TrimSpace(string(value.ExecutionID)) == "" ||
		strings.TrimSpace(string(value.CandidateRevisionID)) == "" ||
		strings.TrimSpace(string(value.BaseRevisionID)) == "" ||
		value.BaseActiveVersion == 0 ||
		value.BaseActiveVersion == ^uint64(0) ||
		value.CandidateRevisionID == value.BaseRevisionID {
		return fmt.Errorf("execution reservation binding is incomplete")
	}
	return nil
}

func reservationTransitionTime(
	reservation ExecutionReservation,
	now time.Time,
) (time.Time, error) {
	if now.IsZero() {
		return time.Time{}, fmt.Errorf("reservation transition time is required")
	}
	transitionAt := now.UTC()
	if transitionAt.Before(reservation.UpdatedAt) {
		return time.Time{}, fmt.Errorf("reservation transition time regresses")
	}
	return transitionAt, nil
}
