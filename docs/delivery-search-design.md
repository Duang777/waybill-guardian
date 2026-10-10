# Deterministic joint search design

This document explains how the built-in delivery solver extends deterministic regret construction
with joint local search. The design covers issues #129 and #130. The public `Solver.Solve` contract
does not change.

## Problem

A route move changes more than distance. It can change hard windows, driver limits, cross-trip SOC,
charger occupancy, active cargo intervals, placements, rehandle work, axle loads, and stability
cost. The solver must reject the whole move when any affected constraint fails.

The independent `validate.Validator` remains the approval authority. Solver checks prune search.
They do not replace the final validation report.

## Caller's view

The caller submits one canonical problem and one replay-bound configuration.

```go
result, err := solver.Solve(ctx, problem, solve.SolveConfig{
	SchemaVersion:    solve.SolveConfigSchemaVersion,
	Strategy:         "deterministic-regret",
	EvaluationBudget: 50_000,
	Seed:             7,
	PlanID:           planID,
	RevisionID:       revisionID,
	ValidationAt:     problem.CreatedAt,
}, progress)
```

The caller may submit `result.Plan` for approval only when `result.Status == SolveCompleted` and
`result.Validation.Valid`.

## One complete candidate state

`domain.Plan` is the candidate representation. Every candidate contains complete duties, trips,
stops, schedules, energy legs, load stages, metrics, and an objective. A unit is assigned through
all required tasks or appears once in `Plan.Unassigned`.

```go
type candidateState struct {
	plan       domain.Plan
	objective  domain.ObjectiveVector
	stateKey   stateKey
	components componentIndex
}

type certifiedState struct {
	candidateState
	report domain.ValidationReport
}
```

`componentIndex` contains derived lookup data and immutable component digests. The plan remains the
source of truth. The solver can rebuild every index from the plan.

`stateKey` hashes the normalized candidate body. The hash covers route order, trip boundaries,
depots, vehicles, drivers, timestamps, breaks, charges, SOC, load stages, placements, rehandles,
metrics, and the objective. Plan IDs, revision IDs, validation time, and artifact digests do not
affect the key.

A separate decision digest can identify compiler inputs for cache reuse. The solver must not use a
decision digest as an exact no-good key because two equal decisions can produce different
materialized states after an input or compiler defect.

## Explicit rehandle artifact

`LoadStage.Rehandles` contains executable operations, not cargo IDs. Each operation records its
one-based sequence, cargo, stop index, complete placement before removal, complete placement after
reload, duration, and direct cost. The planning policy fixes per-cargo duration and cost and limits
the number of operations at one stop.

The packer creates an operation when surviving cargo changes placement or blocks the extraction
corridor of cargo unloaded at the stop. It orders removals from the door inward. The schedule has
one `rehandle` segment per affected stop, and downstream arrival, service, departure, duty, energy,
and cost calculations use the added duration.

The independent Validator reconstructs the same requirements from adjacent load stages and door
geometry. It rejects missing, duplicate, unnecessary, misordered, or stale operations and checks
the schedule duration and direct cost against policy. Solver and Validator use separate
implementations.

## Fixed operator registry

The production registry contains 12 operators in one fixed order.

```text
relocate
swap
2-opt
cross-exchange
trip-split
trip-merge
depot
vehicle
driver
break
charge
placement
```

Each operator emits typed moves in canonical key order. An operator does not decide feasibility,
objective acceptance, or the recomputation boundary.

The registry constructor rejects duplicate, missing, unknown, or reordered operators. Evidence
contains one row for every operator, including operators with zero attempts.

## One coupled evaluator

The evaluator returns either a complete candidate or a typed rejection.

```go
type evaluationResult interface {
	isEvaluationResult()
}

type feasibleCandidate struct {
	state candidateState
}

type rejectedCandidate struct {
	failure failure
	noGood  noGood
}
```

For each move, the evaluator performs these steps:

1. Apply the move to a private copy of the candidate.
2. Derive changed component input digests.
3. Rebuild each affected duty from its earliest changed trip.
4. Recompute route arcs, time, service, waits, breaks, and driver counters.
5. Recompute active cargo intervals, placements, support, extraction, and rehandles.
6. Recompute payload-sensitive energy, cross-trip SOC, charges, and charger occupancy.
7. Recompute metrics, stability cost, the objective, and `stateKey`.

The evaluator derives the dependency closure from component inputs. Operator-provided affected sets
can help diagnostics, but they cannot control correctness.

The full evaluator rebuilds all components. The incremental evaluator reuses a component only when
the component input digest is unchanged. Both evaluators call the same solver-side component
compilers.

Each trip input digest covers the problem digest, vehicle and duty-driver bindings, route decisions,
explicit break anchors, charger choices, stage placement decisions, and the previous trip's end
time, end depot, and final state of charge. Schedule timestamps, energy-leg arithmetic, axle loads,
center of mass, rehandles, metrics, and objectives are compiler outputs and are never cache inputs.

The production evaluator compiles every materialized move twice: once with digest-controlled reuse
and once from an empty component cache. It compares the complete duty and unassigned-unit material
before metric evaluation or certification. A mismatch returns `ErrRecomputeMismatch`; it cannot
become a no-good or a candidate rejection.

The solver and the independent Validator do not share feasibility functions. The final certification
step seals the plan, calls `validate.Validator.Validate`, reconciles metrics and the objective,
reseals the plan, and validates again. Only that step can create `certifiedState`.

An incremental and full recomputation mismatch is an internal failure. A solver and Validator
recomputation mismatch is also an internal failure. Neither mismatch is `manual_review`.

## Deterministic construction

Construction starts with a complete empty plan whose units are provisionally unassigned. It derives
split-safe work items from `SplitPolicy`, task precedence, and `AtomicGroupID`.

Each regret round evaluates all legal insertions for every remaining work item. An insertion can
select a vehicle, a driver, a depot, a trip boundary, a topological task order, and a route position.
The coupled evaluator completes schedule, energy, and loading before ranking the insertion.

The round selects an item in this order:

1. A required item precedes an optional item.
2. An item with one feasible insertion precedes an item with multiple feasible insertions.
3. A larger lexicographic regret precedes a smaller regret.
4. A higher request priority precedes a lower priority.
5. The canonical work item key breaks the tie.

The round is atomic. If the budget ends before all item rankings complete, the solver commits no
candidate from that round. Every pending unit receives `search_exhausted`.

## Strict local improvement

Local search walks the fixed registry and each operator's move keys in order. It accepts the first
candidate for which the complete `domain.ObjectiveVector` is lexicographically smaller than the
incumbent objective. Equal objectives do not replace the incumbent.

After an accepted move, search restarts at `relocate`. A complete pass with no accepted move reaches
a local optimum.

One comparator owns objective ordering for construction, local search, progress, and evidence.

## Budget and terminal status

One budget unit is one complete coupled evaluation. The budget has one solve-local writer. Its
counter never depends on wall time or worker completion order.

The public status and the internal termination reason are separate:

| Search outcome | Public status |
|---|---|
| The fixed budget ends with a certified candidate | `completed` |
| A complete pass finds no improvement and certification succeeds | `completed` |
| The fixed budget ends without a certified candidate | `exhausted` |
| The context is canceled or its deadline expires | `aborted` |
| Hard commitments conflict | `manual_review` |
| A sound presolve certificate proves a contradiction | `infeasible` |
| The strategy or a required capability is unsupported | `unsupported` |

`exhausted` does not mean mathematical infeasibility. A `completed` result can record
`termination = budget_exhausted` in evidence.

Cancellation returns aborted evidence and no approval-ready plan.

## Conservative no-good feedback

Every rejected evaluation records an exact no-good that binds `stateKey`, the move key, and a stable
failure code. Exact no-goods prevent repeated work without pruning a different state.

The solver can create a broader no-good only from typed proof data. Examples include an incompatible
vehicle assignment, an unsupported charger connector, or the same active cargo set failing the same
vehicle geometry. If the proof omits a fact that can affect feasibility, the solver uses the exact
no-good.

No-goods are solve-local and bound to the problem digest, the config digest, and the solver build.

## Evidence

Solve evidence records:

- The budget limit, consumed evaluations, and termination reason.
- The registry digest and terminal search cursor.
- The final incumbent `stateKey`.
- The ordered `stateKey` sequence for accepted candidates.
- Attempts, feasible candidates, rejections, and accepted moves for all 12 operators.
- Learned and applied no-good counts and the no-good digest.
- The route-seed request, response, provider build, and remote job identity when an adapter supplied
  the seed.

Batch width and any inner completion-node limit must affect the config digest or the solver build
identity.

## Module ownership

| File | Responsibility |
|---|---|
| `contract.go` | Public solver, statuses, budget, progress, and versioned evidence |
| `engine.go` | Search transaction, progress, terminal classification, and certification |
| `state.go` | Complete candidate state, indexes, component digests, and `stateKey` |
| `objective.go` | The only objective comparator and regret arithmetic |
| `construction.go` | Work items, insertion enumeration, and atomic regret rounds |
| `operator_registry.go` | Operator IDs, typed moves, registry order, and move keys |
| `operator_route.go` | Relocate, swap, 2-opt, and cross-exchange |
| `operator_trip_resource.go` | Trip split, trip merge, depot, vehicle, and driver moves |
| `operator_schedule_energy.go` | Break and charge moves |
| `operator_load.go` | Placement propagation and explicit rehandle moves |
| `evaluator.go` | Coupled evaluation, certification, and typed failures |
| `recompute.go` | Dependency closure and incremental or full compilation |
| `no_good.go` | Exact and proof-backed no-goods |
| `route_schedule.go` | Route, time, driver, and duty calculations |
| `energy.go` | Cross-trip SOC and charging calculations |
| `packing.go` | Active cargo intervals, placement, extraction, rehandle, axle, and center-of-mass calculations |

## Verification

The implementation must provide these rerunnable checks:

- A registry test that pins all 12 operator IDs and their order.
- A literal objective-order and strict-improvement test.
- One feasible improvement and one joint-feasibility rejection for every operator.
- Incremental and full evaluator equivalence for every generated move.
- A mutation test that removes each dependency edge and proves that equivalence fails.
- A budget replay test for every cutoff from 1 through the complete evaluation count.
- Twenty identical runs after shuffling unordered inputs and changing `GOMAXPROCS`.
- Positive and negative tests for driver limits, charging, multi-trip SOC, and pickup-delivery order.
- One-millimeter geometry tests, support DAG tests, stage continuity tests, explicit rehandle tests,
  and a 300-cargo bounded test.
- Cancellation tests during construction, each operator family, and packing.
- Final certification tests that use the real independent Validator.

## Alternatives

A separate decision model and materialized model gives precise compiler inputs, but it creates two
candidate representations. The selected design keeps `domain.Plan` as the candidate and stores
only derived indexes beside it.

Operator-owned feasibility makes each operator self-contained, but it repeats schedule, energy,
and loading policy across modules. The selected evaluator owns all coupled completion.

Full recomputation is the correctness oracle. It is too expensive as the only production path for
broad neighborhoods and 300-cargo packing, so the production path uses digest-controlled reuse and
proves equivalence against the full evaluator.

Checkpoint persistence remains outside the first local-search implementation. It requires a
separate crash-resume equivalence contract and storage failure semantics.
