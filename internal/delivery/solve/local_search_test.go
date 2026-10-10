package solve

import (
	"context"
	"errors"
	"testing"
)

func TestLocalSearchAcceptsOnlyStrictCertifiedImprovement(t *testing.T) {
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
	plan := normalizeCandidatePlan(solved.Plan)
	trip := &plan.Duties[0].Trips[0]
	tasks := tripTaskIDs(*trip)
	tasks[len(tasks)-2], tasks[len(tasks)-1] =
		tasks[len(tasks)-1], tasks[len(tasks)-2]
	rewriteTripTaskIDs(engine, trip, tasks)
	plan, err = engine.rebuildPlanDuties(context.Background(), plan, []dutyRebuild{{
		DutyIndex: 0,
		FromTrip:  0,
	}})
	if err != nil {
		t.Fatal(err)
	}
	evaluation := engine.evaluateMaterializedPlan(plan)
	bad, ok := evaluation.(feasibleCandidate)
	if !ok {
		t.Fatalf("evaluation = %#v, want feasible candidate", evaluation)
	}
	incumbent, _, err := engine.certifyCandidate(bad.state)
	if err != nil {
		t.Fatal(err)
	}
	improved, err := engine.localSearch(context.Background(), incumbent)
	if err != nil {
		t.Fatal(err)
	}
	if !strictObjectiveImprovement(improved.objective, incumbent.objective) {
		t.Fatalf("objective did not strictly improve: before %+v, after %+v",
			incumbent.objective, improved.objective)
	}
	if !improved.report.Valid {
		t.Fatalf("accepted candidate is not certified: %+v", improved.report.Violations)
	}
	var accepted uint64
	for _, stats := range engine.operatorStats {
		accepted += stats.Accepted
	}
	if accepted == 0 {
		t.Fatal("local search improved the plan without recording an accepted move")
	}
}

func TestBudgetEndingWithCertifiedIncumbentReturnsCompleted(t *testing.T) {
	problem := solverProblem(t)
	config := solveConfig(problem)
	config.EvaluationBudget = 2
	result, err := newTestBuiltin(t).Solve(
		context.Background(),
		problem,
		config,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != SolveCompleted || !result.Validation.Valid {
		t.Fatalf("solve result = %+v, want completed certified incumbent", result)
	}
	if result.Evidence.Termination != TerminationBudgetExhausted ||
		result.Evidence.Evaluations != 2 ||
		result.Evidence.BudgetLimit != 2 {
		t.Fatalf("budget termination evidence = %+v", result.Evidence)
	}
	if result.Plan.Objective.HardViolationCount != 0 ||
		result.Plan.Objective.UnassignedRequiredUnits != 0 {
		t.Fatalf("approval candidate objective = %+v", result.Plan.Objective)
	}
}

func TestProductionOperatorRegistryIsExecutable(t *testing.T) {
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
	got := make([]OperatorID, 0)
	for _, operator := range engine.registry.Operators() {
		got = append(got, operator.ID())
		err := operator.Enumerate(context.Background(), engine, state, func(move searchMove) bool {
			if move.Operator() != operator.ID() || move.Key() == "" {
				t.Fatalf("operator %q emitted invalid move %#v", operator.ID(), move)
			}
			return false
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	want := requiredOperatorIDs()
	if len(got) != len(want) {
		t.Fatalf("executable registry IDs = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("executable registry IDs = %v, want %v", got, want)
		}
	}
}

func TestEveryProductionOperatorPollsCancellationDuringEnumeration(t *testing.T) {
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
	for _, operator := range engine.registry.Operators() {
		t.Run(string(operator.ID()), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := operator.Enumerate(ctx, engine, state, func(searchMove) bool {
				t.Fatal("canceled enumeration emitted a move")
				return false
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Enumerate error = %v, want context cancellation", err)
			}
		})
	}
}
