package solve

import (
	"context"
	"errors"
	"fmt"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func (engine *engine) localSearch(
	ctx context.Context,
	incumbent certifiedState,
) (certifiedState, error) {
	engine.cursor = SearchCursor{Phase: SearchPhaseLocalSearch}
	for {
		accepted := false
		budgetExhausted := false
		var searchErr error
		for _, operator := range engine.registry.Operators() {
			engine.cursor.Operator = operator.ID()
			engine.cursor.MoveKey = ""
			enumerationErr := operator.Enumerate(
				ctx,
				engine,
				incumbent.candidateState,
				func(move searchMove) bool {
					engine.cursor.MoveKey = move.Key()
					candidate, evaluated, exhausted, err := engine.evaluateSearchMove(
						ctx,
						incumbent,
						move,
					)
					if err != nil {
						searchErr = err
						return false
					}
					if exhausted {
						budgetExhausted = true
						return false
					}
					if !evaluated ||
						!strictObjectiveImprovement(candidate.objective, incumbent.objective) {
						return true
					}
					stats := engine.operatorStats[move.Operator()]
					stats.Accepted++
					engine.operatorStats[move.Operator()] = stats
					engine.acceptedStateKeys = append(
						engine.acceptedStateKeys,
						domain.ArtifactDigest(candidate.stateKey),
					)
					incumbent = candidate
					accepted = true
					return false
				},
			)
			if searchErr == nil && enumerationErr != nil {
				searchErr = enumerationErr
			}
			if searchErr != nil || budgetExhausted || accepted {
				break
			}
		}
		if searchErr != nil {
			return certifiedState{}, searchErr
		}
		if budgetExhausted {
			engine.exhausted = true
			engine.termination = TerminationBudgetExhausted
			if err := engine.report(ctx, ProgressImproving, incumbent.objective); err != nil {
				return certifiedState{}, err
			}
			return incumbent, nil
		}
		if accepted {
			if err := engine.report(ctx, ProgressImproving, incumbent.objective); err != nil {
				return certifiedState{}, err
			}
			continue
		}
		engine.termination = TerminationLocalOptimum
		if err := engine.report(ctx, ProgressImproving, incumbent.objective); err != nil {
			return certifiedState{}, err
		}
		return incumbent, nil
	}
}

func (engine *engine) evaluateSearchMove(
	ctx context.Context,
	incumbent certifiedState,
	move searchMove,
) (certifiedState, bool, bool, error) {
	if contextErr := ctx.Err(); contextErr != nil {
		return certifiedState{}, false, false, contextErr
	}
	if _, exists := engine.noGoods.Lookup(incumbent.stateKey, move.Key()); exists {
		return certifiedState{}, false, false, nil
	}
	if _, consumed := engine.budget.Consume(); !consumed {
		return certifiedState{}, false, true, nil
	}
	stats := engine.operatorStats[move.Operator()]
	stats.Attempts++
	engine.operatorStats[move.Operator()] = stats

	plan, err := move.Apply(ctx, engine, incumbent.candidateState)
	if err != nil {
		if errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			return certifiedState{}, false, false, err
		}
		engine.noGoods.Learn(
			incumbent.stateKey,
			move.Key(),
			failureMaterialization,
		)
		engine.recordMoveRejection(move.Operator())
		return certifiedState{}, false, false, nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return certifiedState{}, false, false, contextErr
	}
	plan, err = engine.recomputeMaterializedCandidate(
		ctx,
		incumbent.candidateState,
		plan,
	)
	if err != nil {
		if errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			return certifiedState{}, false, false, err
		}
		if errors.Is(err, ErrRecomputeMismatch) {
			return certifiedState{}, false, false, fmt.Errorf(
				"evaluate move %q: %w",
				move.Key(),
				err,
			)
		}
		engine.noGoods.Learn(
			incumbent.stateKey,
			move.Key(),
			failureMaterialization,
		)
		engine.recordMoveRejection(move.Operator())
		return certifiedState{}, false, false, nil
	}
	evaluation := engine.evaluateMaterializedPlan(plan)
	feasible, ok := evaluation.(feasibleCandidate)
	if !ok {
		engine.noGoods.Learn(
			incumbent.stateKey,
			move.Key(),
			failureMaterialization,
		)
		engine.recordMoveRejection(move.Operator())
		return certifiedState{}, false, false, nil
	}
	candidate, _, err := engine.certifyCandidate(feasible.state)
	if err != nil {
		if errors.Is(err, ErrCandidateRejected) {
			engine.noGoods.Learn(
				incumbent.stateKey,
				move.Key(),
				failureCertification,
			)
			engine.recordMoveRejection(move.Operator())
			return certifiedState{}, false, false, nil
		}
		return certifiedState{}, false, false, fmt.Errorf(
			"evaluate move %q: %w",
			move.Key(),
			err,
		)
	}
	stats = engine.operatorStats[move.Operator()]
	stats.Feasible++
	engine.operatorStats[move.Operator()] = stats
	engine.feasible++
	return candidate, true, false, nil
}

func (engine *engine) recordMoveRejection(operatorID OperatorID) {
	stats := engine.operatorStats[operatorID]
	stats.Rejected++
	engine.operatorStats[operatorID] = stats
	engine.rejected++
}
