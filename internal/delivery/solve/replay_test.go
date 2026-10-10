package solve

import (
	"context"
	"math/rand"
	"runtime"
	"strconv"
	"testing"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/delivery/service"
)

func TestEveryBudgetCutoffReplaysExactly(t *testing.T) {
	problem := solverProblem(t)
	fullConfig := solveConfig(problem)
	full, err := newTestBuiltin(t).Solve(
		context.Background(),
		problem,
		fullConfig,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if full.Evidence.Termination != TerminationLocalOptimum {
		t.Fatalf(
			"reference solve termination = %q, want local optimum",
			full.Evidence.Termination,
		)
	}
	if full.Evidence.Evaluations == 0 {
		t.Fatal("reference solve performed no evaluations")
	}

	for cutoff := EvaluationBudget(1); cutoff <= EvaluationBudget(full.Evidence.Evaluations); cutoff++ {
		t.Run(strconv.FormatUint(uint64(cutoff), 10), func(t *testing.T) {
			config := solveConfig(problem)
			config.EvaluationBudget = cutoff
			first, err := newTestBuiltin(t).Solve(
				context.Background(),
				problem,
				config,
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			second, err := newTestBuiltin(t).Solve(
				context.Background(),
				problem,
				config,
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			if first.Status != second.Status ||
				first.Plan.PlanDigest != second.Plan.PlanDigest ||
				first.Evidence.EvidenceDigest != second.Evidence.EvidenceDigest {
				t.Fatalf(
					"cutoff %d replay differs:\nfirst=%q %q %q\nsecond=%q %q %q",
					cutoff,
					first.Status,
					first.Plan.PlanDigest,
					first.Evidence.EvidenceDigest,
					second.Status,
					second.Plan.PlanDigest,
					second.Evidence.EvidenceDigest,
				)
			}
			if first.Evidence.Evaluations > cutoff {
				t.Fatalf(
					"cutoff %d consumed %d evaluations",
					cutoff,
					first.Evidence.Evaluations,
				)
			}
			if first.Status == SolveCompleted && !first.Validation.Valid {
				t.Fatalf(
					"cutoff %d returned an uncertified completed plan: %+v",
					cutoff,
					first.Validation.Violations,
				)
			}
		})
	}
}

func TestTwentyShuffledRunsRemainBitForBitDeterministic(t *testing.T) {
	base := solverProblem(t)
	config := solveConfig(base)
	want, err := newTestBuiltin(t).Solve(
		context.Background(),
		base,
		config,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	previousProcs := runtime.GOMAXPROCS(0)
	defer runtime.GOMAXPROCS(previousProcs)

	for run := 0; run < 20; run++ {
		runtime.GOMAXPROCS(1 + run%4)
		problem := shuffledProblem(t, base, int64(run+1))
		got, err := newTestBuiltin(t).Solve(
			context.Background(),
			problem,
			solveConfig(problem),
			nil,
		)
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if got.Plan.PlanDigest != want.Plan.PlanDigest ||
			got.Evidence.EvidenceDigest != want.Evidence.EvidenceDigest {
			t.Fatalf(
				"run %d with GOMAXPROCS=%d differs:\nwant plan=%q evidence=%q\ngot  plan=%q evidence=%q",
				run,
				runtime.GOMAXPROCS(0),
				want.Plan.PlanDigest,
				want.Evidence.EvidenceDigest,
				got.Plan.PlanDigest,
				got.Evidence.EvidenceDigest,
			)
		}
	}
}

func shuffledProblem(
	t testing.TB,
	base domain.ProblemSnapshot,
	seed int64,
) domain.ProblemSnapshot {
	t.Helper()
	value := base
	value.Locations = append([]domain.Location{}, base.Locations...)
	value.Depots = append([]domain.Depot{}, base.Depots...)
	value.Requests = append([]domain.TransportRequest{}, base.Requests...)
	value.Units = append([]domain.FulfillmentUnit{}, base.Units...)
	value.Cargo = append([]domain.CargoItem{}, base.Cargo...)
	value.Vehicles = append([]domain.Vehicle{}, base.Vehicles...)
	value.Drivers = append([]domain.Driver{}, base.Drivers...)
	value.Chargers = append([]domain.ChargingStation{}, base.Chargers...)
	value.SourceRefs = append([]domain.SourceRef{}, base.SourceRefs...)

	random := rand.New(rand.NewSource(seed))
	random.Shuffle(len(value.Locations), func(left, right int) {
		value.Locations[left], value.Locations[right] = value.Locations[right], value.Locations[left]
	})
	random.Shuffle(len(value.Depots), func(left, right int) {
		value.Depots[left], value.Depots[right] = value.Depots[right], value.Depots[left]
	})
	random.Shuffle(len(value.Requests), func(left, right int) {
		value.Requests[left], value.Requests[right] = value.Requests[right], value.Requests[left]
	})
	random.Shuffle(len(value.Units), func(left, right int) {
		value.Units[left], value.Units[right] = value.Units[right], value.Units[left]
	})
	random.Shuffle(len(value.Cargo), func(left, right int) {
		value.Cargo[left], value.Cargo[right] = value.Cargo[right], value.Cargo[left]
	})
	random.Shuffle(len(value.Vehicles), func(left, right int) {
		value.Vehicles[left], value.Vehicles[right] = value.Vehicles[right], value.Vehicles[left]
	})
	random.Shuffle(len(value.Drivers), func(left, right int) {
		value.Drivers[left], value.Drivers[right] = value.Drivers[right], value.Drivers[left]
	})
	random.Shuffle(len(value.Chargers), func(left, right int) {
		value.Chargers[left], value.Chargers[right] = value.Chargers[right], value.Chargers[left]
	})
	random.Shuffle(len(value.SourceRefs), func(left, right int) {
		value.SourceRefs[left], value.SourceRefs[right] = value.SourceRefs[right], value.SourceRefs[left]
	})
	value.ProblemDigest = ""
	value.PolicyDigest = ""
	value.CommitmentDigest = ""
	rebuilt, err := service.BuildProblemSnapshot(value)
	if err != nil {
		t.Fatal(err)
	}
	return rebuilt
}
