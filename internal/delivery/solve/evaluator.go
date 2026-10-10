package solve

import (
	"errors"
	"fmt"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

var (
	ErrCandidateRejected = errors.New("candidate rejected")
	ErrRecomputeMismatch = errors.New("recomputation mismatch")
)

type failureCode string

const (
	failureMaterialization failureCode = "materialization"
	failureCertification   failureCode = "certification"
)

type candidateFailure struct {
	code   failureCode
	detail string
}

type evaluationResult interface {
	isEvaluationResult()
}

type feasibleCandidate struct {
	state candidateState
}

func (feasibleCandidate) isEvaluationResult() {}

type rejectedCandidate struct {
	failure candidateFailure
}

func (rejectedCandidate) isEvaluationResult() {}

func (engine *engine) evaluateMaterializedPlan(plan domain.Plan) evaluationResult {
	metrics, err := engine.recomputePlanMetrics(plan)
	if err != nil {
		return rejectedCandidate{failure: candidateFailure{
			code:   failureMaterialization,
			detail: err.Error(),
		}}
	}
	plan.Metrics = metrics
	plan.Objective = engine.objectiveForPlan(plan, metrics)
	if err := sealPlan(&plan); err != nil {
		return rejectedCandidate{failure: candidateFailure{
			code:   failureMaterialization,
			detail: err.Error(),
		}}
	}
	state, err := candidateStateFromPlan(plan)
	if err != nil {
		return rejectedCandidate{failure: candidateFailure{
			code:   failureMaterialization,
			detail: err.Error(),
		}}
	}
	return feasibleCandidate{state: state}
}

func (engine *engine) certifyCandidate(
	state candidateState,
) (certifiedState, domain.ValidationReport, error) {
	probe := engine.validator.Validate(
		engine.problem,
		state.plan,
		engine.config.ValidationAt,
	)
	if probe.Metrics != state.plan.Metrics {
		return certifiedState{}, probe, fmt.Errorf(
			"%w: solver metrics %+v, validator metrics %+v",
			ErrRecomputeMismatch,
			state.plan.Metrics,
			probe.Metrics,
		)
	}

	reconciled := state.plan
	reconciled.Metrics = probe.Metrics
	reconciled.Objective = engine.objectiveForPlan(reconciled, reconciled.Metrics)
	if err := sealPlan(&reconciled); err != nil {
		return certifiedState{}, probe, err
	}
	reconciledState, err := candidateStateFromPlan(reconciled)
	if err != nil {
		return certifiedState{}, probe, err
	}
	if reconciledState.stateKey != state.stateKey {
		return certifiedState{}, probe, fmt.Errorf(
			"%w: certification changed state key from %q to %q",
			ErrRecomputeMismatch,
			state.stateKey,
			reconciledState.stateKey,
		)
	}
	report := engine.validator.Validate(
		engine.problem,
		reconciled,
		engine.config.ValidationAt,
	)
	if report.Metrics != reconciled.Metrics {
		return certifiedState{}, report, fmt.Errorf(
			"%w: reconciled metrics %+v, validator metrics %+v",
			ErrRecomputeMismatch,
			reconciled.Metrics,
			report.Metrics,
		)
	}
	if !report.Valid {
		return certifiedState{}, report, fmt.Errorf(
			"%w: %d validation violations",
			ErrCandidateRejected,
			len(report.Violations),
		)
	}
	return certifiedState{
		candidateState: reconciledState,
		report:         report,
	}, report, nil
}
