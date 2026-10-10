package solve

import (
	"context"
	"fmt"
	"testing"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type rejectingTestMove struct{}

func (rejectingTestMove) Operator() OperatorID {
	return OperatorRelocate
}

func (rejectingTestMove) Key() string {
	return "relocate/rejecting-test"
}

func (rejectingTestMove) Apply(
	context.Context,
	*engine,
	candidateState,
) (domain.Plan, error) {
	return domain.Plan{}, fmt.Errorf("rejected by test move")
}

func TestExactNoGoodSkipsRepeatedMoveWithoutConsumingBudget(t *testing.T) {
	problem := solverProblem(t)
	solved, err := newTestBuiltin(t).Solve(
		context.Background(),
		problem,
		solveConfig(problem),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	engine := newTestEngine(t, problem)
	state, err := candidateStateFromPlan(solved.Plan)
	if err != nil {
		t.Fatal(err)
	}
	incumbent, _, err := engine.certifyCandidate(state)
	if err != nil {
		t.Fatal(err)
	}
	move := rejectingTestMove{}
	_, evaluated, exhausted, err := engine.evaluateSearchMove(
		context.Background(),
		incumbent,
		move,
	)
	if err != nil || evaluated || exhausted {
		t.Fatalf("first evaluation = (%t, %t, %v), want rejected", evaluated, exhausted, err)
	}
	consumed := engine.budget.Consumed()
	_, evaluated, exhausted, err = engine.evaluateSearchMove(
		context.Background(),
		incumbent,
		move,
	)
	if err != nil || evaluated || exhausted {
		t.Fatalf("second evaluation = (%t, %t, %v), want no-good skip",
			evaluated, exhausted, err)
	}
	if engine.budget.Consumed() != consumed {
		t.Fatalf("no-good skip consumed budget: %d -> %d",
			consumed, engine.budget.Consumed())
	}
	stats := engine.operatorStats[OperatorRelocate]
	if stats.Attempts != 1 || stats.Rejected != 1 {
		t.Fatalf("operator stats = %+v, want one rejected evaluation", stats)
	}
	if engine.noGoods.learned != 1 || engine.noGoods.applied != 1 {
		t.Fatalf("no-good counters = learned %d, applied %d",
			engine.noGoods.learned, engine.noGoods.applied)
	}
	digest, err := engine.noGoods.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if !domain.ValidArtifactDigest(digest) {
		t.Fatalf("no-good digest = %q, want SHA-256", digest)
	}
}
