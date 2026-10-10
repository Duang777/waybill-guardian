package service

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/delivery/solve"
	"github.com/Duang777/waybill-guardian/internal/delivery/validate"
)

func TestBuildSuccessorUsesStreamOrderAndBuildsDurableCommitments(t *testing.T) {
	baseTime := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	problem, active, baseFrontier := solvedActivePlan(t, baseTime)
	createdAt := baseTime.Add(10 * time.Minute)
	completed := domain.TaskCompletedFact{
		Meta: factHeader(
			"completion",
			1,
			"fact-completed",
			baseTime.Add(6*time.Minute),
			createdAt,
		),
		TaskID:      "pickup-1",
		VehicleID:   "vehicle-1",
		DriverID:    "driver-1",
		CompletedAt: baseTime.Add(5 * time.Minute),
	}
	projectedAt := baseTime.Add(75 * time.Minute)
	eta := domain.ETADeviationFact{
		Meta: factHeader(
			"eta",
			1,
			"fact-eta",
			baseTime.Add(2*time.Minute),
			createdAt,
		),
		TaskID:             "delivery-1",
		ProjectedServiceAt: projectedAt,
	}
	input := successorInput(
		t,
		problem,
		active,
		baseFrontier,
		[]domain.OperationalFact{eta, completed},
		createdAt,
	)

	outcome, err := BuildSuccessor(input)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Problem.Version != 2 ||
		outcome.Problem.Commitments.BasePlanDigest != active.PlanDigest ||
		outcome.Problem.Commitments.FactWatermark != string(outcome.Frontier.Digest) {
		t.Fatalf("successor identity and frontier = %+v", outcome.Problem.Commitments)
	}
	if got := []domain.FactID{
		outcome.AppliedFacts[0].FactID,
		outcome.AppliedFacts[1].FactID,
	}; !slices.Equal(got, []domain.FactID{"fact-completed", "fact-eta"}) {
		t.Fatalf("applied fact order = %v", got)
	}
	if len(outcome.Problem.Commitments.Executed) != 1 ||
		outcome.Problem.Commitments.Executed[0].TaskID != "pickup-1" {
		t.Fatalf("executed commitments = %+v", outcome.Problem.Commitments.Executed)
	}
	if !hasFrozenTask(outcome.Problem.Commitments.Frozen, "delivery-1") {
		t.Fatalf("frozen commitments = %+v", outcome.Problem.Commitments.Frozen)
	}
	if !hasSoftETA(outcome.Problem.Commitments.Soft, "delivery-1", projectedAt) {
		t.Fatalf("ETA fact did not affect soft commitments: %+v",
			outcome.Problem.Commitments.Soft)
	}
	if len(outcome.Problem.Commitments.InTransit) != 1 ||
		outcome.Problem.Commitments.InTransit[0].CargoID != "cargo-1" {
		t.Fatalf("in-transit commitments = %+v", outcome.Problem.Commitments.InTransit)
	}
	if !domain.ValidArtifactDigest(outcome.ApplicationDigest) ||
		len(outcome.Applications) != 2 {
		t.Fatalf("fact applications = %+v, digest = %q",
			outcome.Applications, outcome.ApplicationDigest)
	}
}

func TestBuildSuccessorRejectsGapDuplicateOnlyAndNilFacts(t *testing.T) {
	baseTime := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	problem, active, baseFrontier := solvedActivePlan(t, baseTime)
	createdAt := baseTime.Add(10 * time.Minute)
	gapFact := domain.ETADeviationFact{
		Meta:               factHeader("eta", 2, "fact-eta-2", baseTime, createdAt),
		TaskID:             "delivery-1",
		ProjectedServiceAt: baseTime.Add(time.Hour),
	}
	gapLedger := mustLedgerFacts(t, []domain.OperationalFact{gapFact})
	gapTarget, err := domain.BuildFactFrontier([]domain.FactPosition{
		gapFact.Meta.Position,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = BuildSuccessor(SuccessorBuildInput{
		BaseProblem:       problem,
		ActiveRevisionID:  active.RevisionID,
		BaseActiveVersion: 1,
		ActivePlan:        active,
		BaseFrontier:      baseFrontier,
		TargetFrontier:    gapTarget,
		Facts:             gapLedger,
		CreatedAt:         createdAt,
	})
	if err == nil {
		t.Fatal("frontier gap was accepted")
	}

	_, err = BuildSuccessor(SuccessorBuildInput{
		BaseProblem:       problem,
		ActiveRevisionID:  active.RevisionID,
		BaseActiveVersion: 1,
		ActivePlan:        active,
		BaseFrontier:      baseFrontier,
		TargetFrontier:    baseFrontier,
		CreatedAt:         createdAt,
	})
	if !errors.Is(err, ErrNoUnappliedFacts) {
		t.Fatalf("duplicate-only successor error = %v", err)
	}

	target, targetErr := domain.BuildFactFrontier([]domain.FactPosition{{
		Stream: factStream("eta"), Epoch: 1, Sequence: 1,
	}})
	if targetErr != nil {
		t.Fatal(targetErr)
	}
	_, err = BuildSuccessor(SuccessorBuildInput{
		BaseProblem:       problem,
		ActiveRevisionID:  active.RevisionID,
		BaseActiveVersion: 1,
		ActivePlan:        active,
		BaseFrontier:      baseFrontier,
		TargetFrontier:    target,
		Facts:             []domain.LedgerFact{{}},
		CreatedAt:         createdAt,
	})
	if err == nil {
		t.Fatal("nil ledger fact was accepted")
	}
}

func TestBuildSuccessorRequiresBoundEpochTransition(t *testing.T) {
	baseTime := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	problem, active, emptyFrontier := solvedActivePlan(t, baseTime)
	firstCreatedAt := baseTime.Add(10 * time.Minute)
	first, err := BuildSuccessor(successorInput(
		t,
		problem,
		active,
		emptyFrontier,
		[]domain.OperationalFact{domain.ETADeviationFact{
			Meta: factHeader(
				"eta",
				1,
				"fact-eta-epoch-1",
				baseTime.Add(time.Minute),
				firstCreatedAt,
			),
			TaskID:             "delivery-1",
			ProjectedServiceAt: baseTime.Add(time.Hour),
		}},
		firstCreatedAt,
	))
	if err != nil {
		t.Fatal(err)
	}
	successorPlan := solveProblem(t, first.Problem, "revision-epoch-1").Plan
	secondCreatedAt := firstCreatedAt.Add(10 * time.Minute)
	opened := domain.StreamOpenedFact{
		Meta: domain.OperationalFactHeader{
			SchemaVersion: domain.OperationalFactSchemaVersion,
			FactID:        "fact-open-epoch-2",
			Position: domain.FactPosition{
				Stream:   factStream("eta"),
				Epoch:    2,
				Sequence: 1,
			},
			OccurredAt: secondCreatedAt.Add(-2 * time.Minute),
			ObservedAt: secondCreatedAt.Add(-time.Minute),
		},
		PreviousEpoch:            1,
		PreviousTerminalSequence: 1,
		PreviousFrontierDigest:   first.Frontier.Digest,
	}
	epochTwoETA := domain.ETADeviationFact{
		Meta: domain.OperationalFactHeader{
			SchemaVersion: domain.OperationalFactSchemaVersion,
			FactID:        "fact-eta-epoch-2",
			Position: domain.FactPosition{
				Stream:   factStream("eta"),
				Epoch:    2,
				Sequence: 2,
			},
			OccurredAt: secondCreatedAt.Add(-time.Minute),
			ObservedAt: secondCreatedAt,
		},
		TaskID:             "delivery-1",
		ProjectedServiceAt: baseTime.Add(2 * time.Hour),
	}

	validInput := successorInput(
		t,
		first.Problem,
		successorPlan,
		first.Frontier,
		[]domain.OperationalFact{epochTwoETA, opened},
		secondCreatedAt,
	)
	validInput.BaseActiveVersion = 2
	outcome, err := BuildSuccessor(validInput)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Frontier.Positions) != 2 ||
		outcome.Frontier.Positions[1].Epoch != 2 ||
		outcome.Frontier.Positions[1].Sequence != 2 ||
		len(outcome.Applications) != 2 {
		t.Fatalf("epoch successor = %+v", outcome)
	}

	opened.PreviousFrontierDigest = outcome.Frontier.Digest
	opened.Meta.FactID = "fact-open-epoch-2-unbound"
	unboundInput := successorInput(
		t,
		first.Problem,
		successorPlan,
		first.Frontier,
		[]domain.OperationalFact{opened, epochTwoETA},
		secondCreatedAt,
	)
	unboundInput.BaseActiveVersion = 2
	if _, err := BuildSuccessor(unboundInput); err == nil {
		t.Fatal("stream-open with wrong previous frontier was accepted")
	}

	opened.PreviousFrontierDigest = first.Frontier.Digest
	opened.Meta.FactID = "fact-open-epoch-2-bundled"
	priorAdvance := domain.ETADeviationFact{
		Meta: factHeader(
			"eta",
			2,
			"fact-eta-epoch-1-second",
			secondCreatedAt.Add(-2*time.Minute),
			secondCreatedAt.Add(-time.Minute),
		),
		TaskID:             "delivery-1",
		ProjectedServiceAt: baseTime.Add(90 * time.Minute),
	}
	bundledInput := successorInput(
		t,
		first.Problem,
		successorPlan,
		first.Frontier,
		[]domain.OperationalFact{priorAdvance, opened},
		secondCreatedAt,
	)
	bundledInput.BaseActiveVersion = 2
	if _, err := BuildSuccessor(bundledInput); err == nil {
		t.Fatal("epoch open bundled with a prior-epoch advance was accepted")
	}
}

func TestBuildSuccessorRejectsRetroactiveEpochInsertion(t *testing.T) {
	baseTime := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	stream := factStream("eta")
	baseFrontier, err := domain.BuildFactFrontier([]domain.FactPosition{{
		Stream: stream, Epoch: 2, Sequence: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	draft := validProblemDraft()
	draft.Commitments.FactWatermark = string(baseFrontier.Digest)
	problem, err := BuildProblemSnapshot(draft)
	if err != nil {
		t.Fatal(err)
	}
	active := solveProblem(t, problem, "revision-retroactive-epoch").Plan
	createdAt := baseTime.Add(10 * time.Minute)
	historical := domain.ETADeviationFact{
		Meta: factHeader(
			"eta",
			1,
			"fact-retroactive-epoch-1",
			baseTime.Add(time.Minute),
			createdAt,
		),
		TaskID:             "delivery-1",
		ProjectedServiceAt: baseTime.Add(time.Hour),
	}
	targetFrontier, err := domain.BuildFactFrontier([]domain.FactPosition{
		historical.Meta.Position,
		{Stream: stream, Epoch: 2, Sequence: 1},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = BuildSuccessor(SuccessorBuildInput{
		BaseProblem:       problem,
		ActiveRevisionID:  active.RevisionID,
		BaseActiveVersion: 1,
		ActivePlan:        active,
		BaseFrontier:      baseFrontier,
		TargetFrontier:    targetFrontier,
		Facts:             mustLedgerFacts(t, []domain.OperationalFact{historical}),
		CreatedAt:         createdAt,
	})
	if err == nil {
		t.Fatal("retroactive fact epoch was accepted")
	}
}

func TestBuildSuccessorRejectsCancellationOfProtectedExecution(t *testing.T) {
	baseTime := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	problem, active, baseFrontier := solvedActivePlan(t, baseTime)
	taskStop := firstTaskStop(t, active)
	createdAt := taskStop.DepartureAt
	cancel := domain.RequestCanceledFact{
		Meta: factHeader(
			"requests",
			1,
			"fact-cancel",
			createdAt.Add(-time.Second),
			createdAt,
		),
		RequestID: "request-1",
	}
	input := successorInput(
		t,
		problem,
		active,
		baseFrontier,
		[]domain.OperationalFact{cancel},
		createdAt,
	)

	_, err := BuildSuccessor(input)
	var manualReview *ManualReviewError
	if !errors.As(err, &manualReview) ||
		manualReview.Code != "cancel_protected_execution" ||
		!slices.Contains(manualReview.Objects, domain.ObjectRef{
			Kind: "request", ID: "request-1",
		}) {
		t.Fatalf("protected cancellation error = %#v", err)
	}
}

func TestBuildSuccessorAllowsCancellationBeforeExecutionStarts(t *testing.T) {
	baseTime := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	problem, active, baseFrontier := solvedActivePlan(t, baseTime)
	createdAt := active.Duties[0].Trips[0].StartAt.Add(-time.Second)
	if !createdAt.After(problem.CreatedAt) {
		createdAt = problem.CreatedAt.Add(time.Second)
	}
	cancel := domain.RequestCanceledFact{
		Meta: factHeader(
			"requests",
			1,
			"fact-cancel",
			createdAt.Add(-time.Second),
			createdAt,
		),
		RequestID: "request-1",
	}
	input := successorInput(
		t,
		problem,
		active,
		baseFrontier,
		[]domain.OperationalFact{cancel},
		createdAt,
	)

	outcome, err := BuildSuccessor(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Problem.Requests) != 0 ||
		len(outcome.Problem.Units) != 0 ||
		len(outcome.Problem.Cargo) != 0 {
		t.Fatalf("untouched request remains in successor: %+v", outcome.Problem)
	}
}

func TestBuildSuccessorAppliesRepeatedCancellationOnce(t *testing.T) {
	baseTime := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	problem, active, baseFrontier := solvedActivePlan(t, baseTime)
	createdAt := active.Duties[0].Trips[0].StartAt.Add(-time.Second)
	if !createdAt.After(problem.CreatedAt) {
		createdAt = problem.CreatedAt.Add(time.Second)
	}
	first := domain.RequestCanceledFact{
		Meta: factHeader(
			"requests",
			1,
			"fact-cancel-1",
			createdAt.Add(-2*time.Second),
			createdAt.Add(-time.Second),
		),
		RequestID: "request-1",
	}
	second := domain.RequestCanceledFact{
		Meta: factHeader(
			"requests",
			2,
			"fact-cancel-2",
			createdAt.Add(-time.Second),
			createdAt,
		),
		RequestID: "request-1",
	}

	outcome, err := BuildSuccessor(successorInput(
		t,
		problem,
		active,
		baseFrontier,
		[]domain.OperationalFact{second, first},
		createdAt,
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Problem.Requests) != 0 ||
		len(outcome.Problem.Units) != 0 ||
		len(outcome.Problem.Cargo) != 0 {
		t.Fatalf("repeated cancellation left request graph: %+v", outcome.Problem)
	}
	if len(outcome.Applications) != 2 ||
		outcome.Applications[0].Fact.FactID != "fact-cancel-1" ||
		outcome.Applications[1].Fact.FactID != "fact-cancel-2" {
		t.Fatalf("cancellation applications = %+v", outcome.Applications)
	}
}

func TestBuildSuccessorRejectsCrossStreamSemanticConflict(t *testing.T) {
	baseTime := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	problem, active, baseFrontier := solvedActivePlan(t, baseTime)
	createdAt := baseTime.Add(10 * time.Minute)
	first := domain.ETADeviationFact{
		Meta:               factHeader("eta-a", 1, "fact-eta-a", baseTime, createdAt),
		TaskID:             "delivery-1",
		ProjectedServiceAt: baseTime.Add(time.Hour),
	}
	second := domain.ETADeviationFact{
		Meta:               factHeader("eta-b", 1, "fact-eta-b", baseTime, createdAt),
		TaskID:             "delivery-1",
		ProjectedServiceAt: baseTime.Add(2 * time.Hour),
	}
	input := successorInput(
		t,
		problem,
		active,
		baseFrontier,
		[]domain.OperationalFact{second, first},
		createdAt,
	)

	_, err := BuildSuccessor(input)
	var conflict *SemanticFactConflictError
	if !errors.As(err, &conflict) ||
		conflict.Key != "task/delivery-1/projected_service_at" {
		t.Fatalf("semantic conflict error = %#v", err)
	}
}

func TestSuccessorSolvePreservesProtectedCommitments(t *testing.T) {
	baseTime := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	problem, active, baseFrontier := solvedActivePlan(t, baseTime)
	createdAt := baseTime.Add(10 * time.Minute)
	completed := domain.TaskCompletedFact{
		Meta: factHeader(
			"completion",
			1,
			"fact-completed",
			baseTime.Add(6*time.Minute),
			createdAt,
		),
		TaskID:      "pickup-1",
		VehicleID:   "vehicle-1",
		DriverID:    "driver-1",
		CompletedAt: baseTime.Add(5 * time.Minute),
	}
	outcome, err := BuildSuccessor(successorInput(
		t,
		problem,
		active,
		baseFrontier,
		[]domain.OperationalFact{completed},
		createdAt,
	))
	if err != nil {
		t.Fatal(err)
	}
	result := solveProblem(t, outcome.Problem, "revision-successor")
	if result.Status != solve.SolveCompleted || !result.Validation.Valid {
		t.Fatalf("successor solve status = %q, violations = %+v",
			result.Status, result.Validation.Violations)
	}
}

func TestBuildSuccessorDeliveryCompletionReleasesInTransitCargo(t *testing.T) {
	baseTime := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	problem, initialPlan, baseFrontier := solvedActivePlan(t, baseTime)
	placement := initialPlan.Duties[0].Trips[0].LoadStages[0].Placements[0]
	problem.Commitments.InTransit = []domain.InTransitCargoCommitment{{
		CargoID:       "cargo-1",
		VehicleID:     initialPlan.Duties[0].VehicleID,
		CompartmentID: placement.CompartmentID,
	}}
	problem.ProblemDigest = ""
	problem.PolicyDigest = ""
	problem.CommitmentDigest = ""
	problem, err := BuildProblemSnapshot(problem)
	if err != nil {
		t.Fatal(err)
	}
	active := solveProblem(t, problem, "revision-in-transit").Plan
	visit := taskVisits(active)["delivery-1"]
	if len(visit.driverIDs) == 0 {
		t.Fatal("delivery visit has no driver")
	}
	createdAt := visit.departureAt
	completed := domain.TaskCompletedFact{
		Meta: factHeader(
			"completion",
			1,
			"fact-delivery-completed",
			visit.serviceAt,
			createdAt,
		),
		TaskID:      "delivery-1",
		VehicleID:   visit.vehicleID,
		DriverID:    visit.driverIDs[0],
		CompletedAt: visit.serviceAt,
	}

	outcome, err := BuildSuccessor(successorInput(
		t,
		problem,
		active,
		baseFrontier,
		[]domain.OperationalFact{completed},
		createdAt,
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Problem.Commitments.InTransit) != 0 {
		t.Fatalf(
			"delivered cargo remains in transit: %+v",
			outcome.Problem.Commitments.InTransit,
		)
	}
	if len(outcome.Problem.Commitments.Executed) != 1 ||
		outcome.Problem.Commitments.Executed[0].TaskID != "delivery-1" {
		t.Fatalf(
			"delivery completion was not retained: %+v",
			outcome.Problem.Commitments.Executed,
		)
	}
}

func solvedActivePlan(
	t testing.TB,
	baseTime time.Time,
) (domain.ProblemSnapshot, domain.Plan, domain.FactFrontier) {
	t.Helper()
	frontier, err := domain.BuildFactFrontier(nil)
	if err != nil {
		t.Fatal(err)
	}
	draft := validProblemDraft()
	draft.Commitments.FactWatermark = string(frontier.Digest)
	problem, err := BuildProblemSnapshot(draft)
	if err != nil {
		t.Fatal(err)
	}
	solver, err := solve.NewBuiltin(
		domain.SolverIdentity{Name: "builtin", Version: "1.0.0", Build: "test"},
		validate.New(domain.ValidatorIdentity{
			Name: "independent", Version: "1.0.0", Build: "test",
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := solver.Solve(context.Background(), problem, solve.SolveConfig{
		SchemaVersion:    solve.SolveConfigSchemaVersion,
		Strategy:         "deterministic-regret",
		EvaluationBudget: 100,
		Seed:             1,
		PlanID:           "plan-active",
		RevisionID:       "revision-active",
		ValidationAt:     baseTime.Add(-time.Minute),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != solve.SolveCompleted {
		t.Fatalf("active solve status = %q, violations = %+v",
			result.Status, result.Validation.Violations)
	}
	return problem, result.Plan, frontier
}

func successorInput(
	t testing.TB,
	problem domain.ProblemSnapshot,
	active domain.Plan,
	baseFrontier domain.FactFrontier,
	facts []domain.OperationalFact,
	createdAt time.Time,
) SuccessorBuildInput {
	t.Helper()
	ledgerFacts := mustLedgerFacts(t, facts)
	positions := append([]domain.FactPosition(nil), baseFrontier.Positions...)
	heads := make(map[struct {
		stream domain.FactStream
		epoch  uint64
	}]domain.FactPosition)
	for _, position := range positions {
		heads[struct {
			stream domain.FactStream
			epoch  uint64
		}{position.Stream, position.Epoch}] = position
	}
	for _, fact := range ledgerFacts {
		position := fact.Ref().Position
		key := struct {
			stream domain.FactStream
			epoch  uint64
		}{position.Stream, position.Epoch}
		if current, exists := heads[key]; !exists ||
			position.Sequence > current.Sequence {
			heads[key] = position
		}
	}
	positions = positions[:0]
	for _, position := range heads {
		positions = append(positions, position)
	}
	target, err := domain.BuildFactFrontier(positions)
	if err != nil {
		t.Fatal(err)
	}
	return SuccessorBuildInput{
		BaseProblem:       problem,
		ActiveRevisionID:  active.RevisionID,
		BaseActiveVersion: 1,
		ActivePlan:        active,
		BaseFrontier:      baseFrontier,
		TargetFrontier:    target,
		Facts:             ledgerFacts,
		CreatedAt:         createdAt,
	}
}

func mustLedgerFacts(
	t testing.TB,
	facts []domain.OperationalFact,
) []domain.LedgerFact {
	t.Helper()
	result := make([]domain.LedgerFact, len(facts))
	for index, fact := range facts {
		ledgerFact, err := domain.BuildLedgerFact(fact)
		if err != nil {
			t.Fatal(err)
		}
		result[index] = ledgerFact
	}
	slices.SortFunc(result, compareLedgerFacts)
	return result
}

func solveProblem(
	t testing.TB,
	problem domain.ProblemSnapshot,
	revisionID domain.PlanRevisionID,
) solve.SolveResult {
	t.Helper()
	solver, err := solve.NewBuiltin(
		domain.SolverIdentity{Name: "builtin", Version: "1.0.0", Build: "test"},
		validate.New(domain.ValidatorIdentity{
			Name: "independent", Version: "1.0.0", Build: "test",
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := solver.Solve(context.Background(), problem, solve.SolveConfig{
		SchemaVersion:    solve.SolveConfigSchemaVersion,
		Strategy:         "deterministic-regret",
		EvaluationBudget: 100,
		Seed:             1,
		PlanID:           "plan-active",
		RevisionID:       revisionID,
		ValidationAt:     problem.CreatedAt,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func firstTaskStop(t testing.TB, active domain.Plan) domain.Stop {
	t.Helper()
	for _, duty := range active.Duties {
		for _, trip := range duty.Trips {
			for _, stop := range trip.Stops {
				if len(stop.TaskIDs) > 0 {
					return stop
				}
			}
		}
	}
	t.Fatal("active plan has no task stop")
	return domain.Stop{}
}

func hasFrozenTask(
	values []domain.FrozenTaskCommitment,
	taskID domain.TaskID,
) bool {
	for _, value := range values {
		if value.TaskID == taskID {
			return true
		}
	}
	return false
}

func hasSoftETA(
	values []domain.SoftTaskCommitment,
	taskID domain.TaskID,
	at time.Time,
) bool {
	for _, value := range values {
		if value.TaskID == taskID && value.PlannedServiceAt.Equal(at) {
			return true
		}
	}
	return false
}

func factStream(name string) domain.FactStream {
	return domain.FactStream{
		SourceSystem: "operations",
		Name:         name,
		Partition:    "east",
	}
}

func factHeader(
	streamName string,
	sequence uint64,
	id domain.FactID,
	occurredAt time.Time,
	observedAt time.Time,
) domain.OperationalFactHeader {
	return domain.OperationalFactHeader{
		SchemaVersion: domain.OperationalFactSchemaVersion,
		FactID:        id,
		Position: domain.FactPosition{
			Stream:   factStream(streamName),
			Epoch:    1,
			Sequence: sequence,
		},
		OccurredAt: occurredAt,
		ObservedAt: observedAt,
	}
}
