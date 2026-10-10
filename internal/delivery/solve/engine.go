package solve

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/delivery/validate"
)

type engine struct {
	problem       domain.ProblemSnapshot
	config        SolveConfig
	configDigest  domain.ArtifactDigest
	identity      domain.SolverIdentity
	validator     validate.Validator
	sink          ProgressSink
	routeSeed     *RouteSeed
	index         problemIndex
	registry      operatorRegistry
	operatorStats map[OperatorID]OperatorEvaluation
	noGoods       noGoodStore

	budget               evaluationCounter
	constructionAttempts uint64
	feasible             uint64
	accepted             uint64
	rejected             uint64
	exhausted            bool
	termination          TerminationReason
	cursor               SearchCursor
	acceptedStateKeys    []domain.ArtifactDigest
}

type problemIndex struct {
	depots        map[domain.DepotID]domain.Depot
	locations     map[domain.LocationID]domain.Location
	requests      map[domain.RequestID]domain.TransportRequest
	tasks         map[domain.TaskID]domain.ServiceTask
	taskRequests  map[domain.TaskID]domain.RequestID
	units         map[domain.FulfillmentUnitID]domain.FulfillmentUnit
	cargo         map[domain.CargoID]domain.CargoItem
	vehicles      map[domain.VehicleID]domain.Vehicle
	drivers       map[domain.DriverID]domain.Driver
	chargers      map[domain.ChargerID]domain.ChargingStation
	travelIndex   map[domain.LocationID]int
	energyIndex   map[domain.LocationID]int
	energyProfile map[string]domain.EnergyProfileMatrix
}

type candidate struct {
	vehicle     domain.Vehicle
	driver      domain.Driver
	trip        domain.Trip
	replaceLast bool
	state       candidateState
	score       candidateScore
}

type candidateScore struct {
	objective   domain.ObjectiveVector
	seedPenalty int
	stateKey    stateKey
	vehicleID   domain.VehicleID
	driverID    domain.DriverID
}

type candidateRanking struct {
	best     candidate
	second   candidate
	feasible uint64
	complete bool
}

type rankedWorkItem struct {
	index   int
	item    workItem
	ranking candidateRanking
}

func newEngine(
	problem domain.ProblemSnapshot,
	config SolveConfig,
	configDigest domain.ArtifactDigest,
	identity domain.SolverIdentity,
	validator validate.Validator,
	sink ProgressSink,
) (*engine, error) {
	digest, err := domain.ComputeProblemDigest(problem)
	if err != nil {
		return nil, fmt.Errorf("digest problem: %w", err)
	}
	if !domain.ValidArtifactDigest(problem.ProblemDigest) || digest != problem.ProblemDigest {
		return nil, fmt.Errorf("problem digest does not match canonical snapshot")
	}
	registry, err := newProductionOperatorRegistry()
	if err != nil {
		return nil, err
	}
	operatorStats := make(map[OperatorID]OperatorEvaluation, len(requiredOperatorOrder))
	for _, operatorID := range requiredOperatorOrder {
		operatorStats[operatorID] = OperatorEvaluation{Operator: operatorID}
	}
	return &engine{
		problem:       problem,
		config:        config,
		configDigest:  configDigest,
		identity:      identity,
		validator:     validator,
		sink:          sink,
		index:         buildProblemIndex(problem),
		registry:      registry,
		operatorStats: operatorStats,
		noGoods:       newNoGoodStore(problem.ProblemDigest, configDigest, identity),
		budget:        newEvaluationCounter(config.EvaluationBudget),
		termination:   TerminationConstructionComplete,
		cursor:        SearchCursor{Phase: SearchPhaseConstruction},
	}, nil
}

func buildProblemIndex(problem domain.ProblemSnapshot) problemIndex {
	index := problemIndex{
		depots:        make(map[domain.DepotID]domain.Depot, len(problem.Depots)),
		locations:     make(map[domain.LocationID]domain.Location, len(problem.Locations)),
		requests:      make(map[domain.RequestID]domain.TransportRequest, len(problem.Requests)),
		tasks:         make(map[domain.TaskID]domain.ServiceTask),
		taskRequests:  make(map[domain.TaskID]domain.RequestID),
		units:         make(map[domain.FulfillmentUnitID]domain.FulfillmentUnit, len(problem.Units)),
		cargo:         make(map[domain.CargoID]domain.CargoItem, len(problem.Cargo)),
		vehicles:      make(map[domain.VehicleID]domain.Vehicle, len(problem.Vehicles)),
		drivers:       make(map[domain.DriverID]domain.Driver, len(problem.Drivers)),
		chargers:      make(map[domain.ChargerID]domain.ChargingStation, len(problem.Chargers)),
		travelIndex:   make(map[domain.LocationID]int, len(problem.Travel.NodeIDs)),
		energyIndex:   make(map[domain.LocationID]int, len(problem.Energy.NodeIDs)),
		energyProfile: make(map[string]domain.EnergyProfileMatrix, len(problem.Energy.Profiles)),
	}
	for _, value := range problem.Depots {
		index.depots[value.ID] = value
	}
	for _, value := range problem.Locations {
		index.locations[value.ID] = value
	}
	for _, request := range problem.Requests {
		index.requests[request.ID] = request
		for _, task := range request.Tasks {
			index.tasks[task.ID] = task
			index.taskRequests[task.ID] = request.ID
		}
	}
	for _, value := range problem.Units {
		index.units[value.ID] = value
	}
	for _, value := range problem.Cargo {
		index.cargo[value.ID] = value
	}
	for _, value := range problem.Vehicles {
		index.vehicles[value.ID] = value
	}
	for _, value := range problem.Drivers {
		index.drivers[value.ID] = value
	}
	for _, value := range problem.Chargers {
		index.chargers[value.ID] = value
	}
	for position, value := range problem.Travel.NodeIDs {
		index.travelIndex[value] = position
	}
	for position, value := range problem.Energy.NodeIDs {
		index.energyIndex[value] = position
	}
	for _, value := range problem.Energy.Profiles {
		index.energyProfile[value.ProfileID] = value
	}
	return index
}

func (engine *engine) solve(ctx context.Context) (SolveResult, error) {
	if err := engine.report(ctx, ProgressBaseline, domain.ObjectiveVector{}); err != nil {
		return engine.abort(err)
	}

	requests := append([]domain.TransportRequest(nil), engine.problem.Requests...)
	slices.SortFunc(requests, compareRequests)
	items := make([]workItem, 0, len(requests))
	for _, request := range requests {
		fragments, err := requestWorkItems(request, engine.index.units)
		if err != nil {
			return SolveResult{}, err
		}
		items = append(items, fragments...)
	}
	duties := make(map[domain.VehicleID]domain.VehicleDuty)
	driverVehicle := make(map[domain.DriverID]domain.VehicleID)
	requestVehicle := make(map[domain.RequestID]domain.VehicleID)
	unassigned := make([]domain.UnassignedUnit, 0)

	for len(items) > 0 {
		if err := ctx.Err(); err != nil {
			return engine.abort(err)
		}
		rankings := make([]rankedWorkItem, 0, len(items))
		roundComplete := true
		for index, item := range items {
			ranking, err := engine.rankCandidates(
				ctx,
				item.request,
				requestVehicle[item.requestID],
				duties,
				driverVehicle,
				items,
				unassigned,
			)
			if err != nil {
				return engine.abort(err)
			}
			if !ranking.complete {
				engine.exhausted = true
				engine.termination = TerminationBudgetExhausted
				roundComplete = false
				break
			}
			rankings = append(rankings, rankedWorkItem{
				index: index, item: item, ranking: ranking,
			})
		}
		if !roundComplete {
			appendUnassigned(
				&unassigned,
				items,
				domain.UnassignedSearchExhausted,
				"evaluation budget exhausted before the candidate round completed",
			)
			break
		}

		remove := make(map[int]struct{})
		var selected rankedWorkItem
		selectedFound := false
		var roundObjective domain.ObjectiveVector
		for _, current := range rankings {
			if current.ranking.feasible == 0 {
				appendUnassigned(
					&unassigned,
					[]workItem{current.item},
					domain.UnassignedNoVehicle,
					"no complete route, schedule, energy, and loading candidate",
				)
				remove[current.index] = struct{}{}
				continue
			}
			if !selectedFound || compareRankedWorkItems(current, selected) < 0 {
				selected = current
				selectedFound = true
			}
		}
		if selectedFound {
			engine.applyCandidate(
				selected.item,
				selected.ranking.best,
				duties,
				driverVehicle,
				requestVehicle,
			)
			engine.accepted++
			roundObjective = selected.ranking.best.state.objective
			remove[selected.index] = struct{}{}
		}
		next := make([]workItem, 0, len(items)-len(remove))
		for index, item := range items {
			if _, removed := remove[index]; !removed {
				next = append(next, item)
			}
		}
		if len(next) == len(items) {
			return SolveResult{}, fmt.Errorf("candidate round made no progress")
		}
		items = next
		if err := engine.report(ctx, ProgressRouting, roundObjective); err != nil {
			return engine.abort(err)
		}
	}

	plan := engine.buildPlan(duties, unassigned)
	result, err := engine.finalize(ctx, plan)
	if err != nil {
		return SolveResult{}, err
	}
	if err := engine.report(ctx, ProgressValidating, result.Plan.Objective); err != nil {
		return engine.abort(err)
	}
	return result, nil
}

func compareRequests(left, right domain.TransportRequest) int {
	if left.Required != right.Required {
		if left.Required {
			return -1
		}
		return 1
	}
	if left.Priority != right.Priority {
		if left.Priority > right.Priority {
			return -1
		}
		return 1
	}
	return strings.Compare(string(left.ID), string(right.ID))
}

func (engine *engine) rankCandidates(
	ctx context.Context,
	request domain.TransportRequest,
	lockedVehicle domain.VehicleID,
	duties map[domain.VehicleID]domain.VehicleDuty,
	driverVehicle map[domain.DriverID]domain.VehicleID,
	pending []workItem,
	unassigned []domain.UnassignedUnit,
) (candidateRanking, error) {
	ranking := candidateRanking{complete: true}
	for _, vehicle := range engine.problem.Vehicles {
		if lockedVehicle != "" && vehicle.ID != lockedVehicle {
			continue
		}
		duty := duties[vehicle.ID]
		if !containsSkills(vehicle.Skills, request.RequiredSkills) {
			continue
		}
		for _, driver := range engine.problem.Drivers {
			if assignedVehicle, assigned := driverVehicle[driver.ID]; assigned &&
				assignedVehicle != vehicle.ID {
				continue
			}
			if len(duty.DriverIDs) > 0 && !slices.Contains(duty.DriverIDs, driver.ID) {
				continue
			}
			if !containsSkills(driver.Skills, request.RequiredSkills) {
				continue
			}
			if len(duty.Trips) > 0 {
				combined := engine.combineTripRequest(
					duty.Trips[len(duty.Trips)-1],
					request,
				)
				baseDuty := duty
				baseDuty.Trips = append(
					[]domain.Trip(nil),
					duty.Trips[:len(duty.Trips)-1]...,
				)
				current, evaluated, exhausted, err := engine.evaluateCandidate(
					ctx,
					combined,
					request,
					vehicle,
					driver,
					baseDuty,
					true,
					duties,
					pending,
					unassigned,
				)
				if err != nil {
					return candidateRanking{}, err
				}
				if exhausted {
					ranking.complete = false
					return ranking, nil
				}
				if evaluated {
					ranking.consider(current)
				}
			}
			if len(duty.Trips) >= int(vehicle.MaxTrips) {
				continue
			}
			current, evaluated, exhausted, err := engine.evaluateCandidate(
				ctx,
				request,
				request,
				vehicle,
				driver,
				duty,
				false,
				duties,
				pending,
				unassigned,
			)
			if err != nil {
				return candidateRanking{}, err
			}
			if exhausted {
				ranking.complete = false
				return ranking, nil
			}
			if evaluated {
				ranking.consider(current)
			}
		}
	}
	return ranking, nil
}

func (engine *engine) evaluateCandidate(
	ctx context.Context,
	buildRequest domain.TransportRequest,
	seedRequest domain.TransportRequest,
	vehicle domain.Vehicle,
	driver domain.Driver,
	duty domain.VehicleDuty,
	replaceLast bool,
	duties map[domain.VehicleID]domain.VehicleDuty,
	pending []workItem,
	unassigned []domain.UnassignedUnit,
) (candidate, bool, bool, error) {
	if _, consumed := engine.budget.Consume(); !consumed {
		return candidate{}, false, true, nil
	}
	engine.constructionAttempts++
	if err := ctx.Err(); err != nil {
		return candidate{}, false, false, err
	}
	trip, err := engine.buildTrip(ctx, buildRequest, vehicle, driver, duty)
	if err != nil {
		if errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			return candidate{}, false, false, err
		}
		engine.rejected++
		return candidate{}, false, false, nil
	}
	candidateDuty := duty
	candidateDuty.VehicleID = vehicle.ID
	if !slices.Contains(candidateDuty.DriverIDs, driver.ID) {
		candidateDuty.DriverIDs = append(candidateDuty.DriverIDs, driver.ID)
		slices.Sort(candidateDuty.DriverIDs)
	}
	candidateDuty.Trips = append(
		append([]domain.Trip{}, candidateDuty.Trips...),
		trip,
	)
	candidateDuties := make(map[domain.VehicleID]domain.VehicleDuty, len(duties)+1)
	for vehicleID, existing := range duties {
		candidateDuties[vehicleID] = existing
	}
	candidateDuties[vehicle.ID] = candidateDuty
	plan := engine.buildPlan(
		candidateDuties,
		constructionUnassigned(unassigned, pending, seedRequest.UnitIDs),
	)
	evaluation := engine.evaluateMaterializedPlan(plan)
	feasible, ok := evaluation.(feasibleCandidate)
	if !ok {
		engine.rejected++
		return candidate{}, false, false, nil
	}
	engine.feasible++
	score := candidateScore{
		objective:   feasible.state.objective,
		seedPenalty: engine.seedPenalty(seedRequest, vehicle.ID, driver.ID),
		stateKey:    feasible.state.stateKey,
		vehicleID:   vehicle.ID,
		driverID:    driver.ID,
	}
	return candidate{
		vehicle:     vehicle,
		driver:      driver,
		trip:        trip,
		replaceLast: replaceLast,
		state:       feasible.state,
		score:       score,
	}, true, false, nil
}

func (ranking *candidateRanking) consider(value candidate) {
	ranking.feasible++
	if ranking.feasible == 1 ||
		compareCandidateScore(value.score, ranking.best.score) < 0 {
		if ranking.feasible > 1 {
			ranking.second = ranking.best
		}
		ranking.best = value
		return
	}
	if ranking.feasible == 2 ||
		compareCandidateScore(value.score, ranking.second.score) < 0 {
		ranking.second = value
	}
}

func compareRankedWorkItems(left, right rankedWorkItem) int {
	if left.item.request.Required != right.item.request.Required {
		if left.item.request.Required {
			return -1
		}
		return 1
	}
	if result := compareCandidateRegret(left.ranking, right.ranking); result != 0 {
		return result
	}
	if left.item.request.Priority != right.item.request.Priority {
		if left.item.request.Priority > right.item.request.Priority {
			return -1
		}
		return 1
	}
	if result := strings.Compare(
		string(left.item.requestID),
		string(right.item.requestID),
	); result != 0 {
		return result
	}
	return left.index - right.index
}

func compareCandidateRegret(left, right candidateRanking) int {
	leftSingle := left.feasible == 1
	rightSingle := right.feasible == 1
	if leftSingle != rightSingle {
		if leftSingle {
			return -1
		}
		return 1
	}
	if leftSingle {
		return 0
	}
	leftGap := objectiveRegretBetween(
		left.second.score.objective,
		left.best.score.objective,
	)
	rightGap := objectiveRegretBetween(
		right.second.score.objective,
		right.best.score.objective,
	)
	if result := compareObjectiveRegret(leftGap, rightGap); result != 0 {
		return -result
	}
	leftSeedGap := left.second.score.seedPenalty - left.best.score.seedPenalty
	rightSeedGap := right.second.score.seedPenalty - right.best.score.seedPenalty
	if leftSeedGap > rightSeedGap {
		return -1
	}
	if leftSeedGap < rightSeedGap {
		return 1
	}
	return 0
}

func compareCandidateScore(left, right candidateScore) int {
	if result := compareObjective(left.objective, right.objective); result != 0 {
		return result
	}
	switch {
	case left.seedPenalty != right.seedPenalty:
		return left.seedPenalty - right.seedPenalty
	default:
		if result := strings.Compare(string(left.vehicleID), string(right.vehicleID)); result != 0 {
			return result
		}
		if result := strings.Compare(string(left.driverID), string(right.driverID)); result != 0 {
			return result
		}
		return strings.Compare(string(left.stateKey), string(right.stateKey))
	}
}

func (engine *engine) seedPenalty(
	request domain.TransportRequest,
	vehicleID domain.VehicleID,
	driverID domain.DriverID,
) int {
	if engine.routeSeed == nil {
		return 0
	}
	taskSet := make(map[domain.TaskID]struct{}, len(request.Tasks))
	for _, task := range request.Tasks {
		taskSet[task.ID] = struct{}{}
	}
	for _, route := range engine.routeSeed.Routes {
		matches := false
		for _, taskID := range route.TaskIDs {
			if _, exists := taskSet[taskID]; exists {
				matches = true
				break
			}
		}
		if !matches {
			continue
		}
		if route.VehicleID == vehicleID &&
			(route.DriverID == "" || route.DriverID == driverID) {
			return 0
		}
		return 1
	}
	return 0
}

func (engine *engine) buildPlan(
	dutiesByVehicle map[domain.VehicleID]domain.VehicleDuty,
	unassigned []domain.UnassignedUnit,
) domain.Plan {
	duties := make([]domain.VehicleDuty, 0, len(dutiesByVehicle))
	for _, duty := range dutiesByVehicle {
		duties = append(duties, duty)
	}
	slices.SortFunc(duties, func(left, right domain.VehicleDuty) int {
		return strings.Compare(string(left.VehicleID), string(right.VehicleID))
	})
	slices.SortFunc(unassigned, func(left, right domain.UnassignedUnit) int {
		return strings.Compare(string(left.UnitID), string(right.UnitID))
	})
	return domain.Plan{
		SchemaVersion:    domain.PlanSchemaVersion,
		PlanID:           engine.config.PlanID,
		RevisionID:       engine.config.RevisionID,
		ProblemDigest:    engine.problem.ProblemDigest,
		PolicyDigest:     engine.problem.PolicyDigest,
		CommitmentDigest: engine.problem.CommitmentDigest,
		Solver:           engine.identity,
		ConfigDigest:     engine.configDigest,
		Duties:           duties,
		Unassigned:       unassigned,
	}
}

func (engine *engine) finalize(
	ctx context.Context,
	plan domain.Plan,
) (SolveResult, error) {
	evaluation := engine.evaluateMaterializedPlan(plan)
	feasible, ok := evaluation.(feasibleCandidate)
	if !ok {
		rejected := evaluation.(rejectedCandidate)
		return SolveResult{}, fmt.Errorf(
			"evaluate final plan: %w: %s",
			ErrCandidateRejected,
			rejected.failure.detail,
		)
	}
	state := feasible.state
	certified, report, certificationErr := engine.certifyCandidate(state)
	if certificationErr == nil {
		certified, certificationErr = engine.localSearch(ctx, certified)
		if certificationErr != nil {
			return SolveResult{}, certificationErr
		}
		state = certified.candidateState
		report = certified.report
	} else if !errors.Is(certificationErr, ErrCandidateRejected) {
		return SolveResult{}, certificationErr
	}
	plan = state.plan
	status := SolveCompleted
	if !report.Valid {
		status = SolveInfeasible
		engine.termination = TerminationInfeasible
		for _, violation := range report.Violations {
			if strings.HasPrefix(violation.Code, "V11") {
				status = SolveManualReview
				engine.termination = TerminationCommitmentConflict
				break
			}
		}
		if status == SolveInfeasible && engine.exhausted {
			status = SolveExhausted
			engine.termination = TerminationBudgetExhausted
		}
	}
	evidence, err := engine.buildEvidence(
		status,
		conflictSet(report),
		domain.ArtifactDigest(state.stateKey),
	)
	if err != nil {
		return SolveResult{}, err
	}
	return SolveResult{
		Status: status, Plan: plan, Validation: report, Evidence: evidence,
	}, nil
}

func sealPlan(plan *domain.Plan) error {
	plan.PlanDigest = ""
	digest, err := domain.ComputePlanDigest(*plan)
	if err != nil {
		return fmt.Errorf("digest solved plan: %w", err)
	}
	plan.PlanDigest = digest
	return nil
}

func (engine *engine) unassignedRequired(values []domain.UnassignedUnit) uint32 {
	var count uint32
	for _, value := range values {
		unit, exists := engine.index.units[value.UnitID]
		if !exists {
			continue
		}
		if engine.index.requests[unit.RequestID].Required {
			count++
		}
	}
	return count
}

func conflictSet(report domain.ValidationReport) []domain.ObjectRef {
	seen := make(map[domain.ObjectRef]struct{})
	result := make([]domain.ObjectRef, 0)
	for _, violation := range report.Violations {
		if violation.Severity != domain.SeverityError {
			continue
		}
		if _, exists := seen[violation.Object]; !exists {
			seen[violation.Object] = struct{}{}
			result = append(result, violation.Object)
		}
		for _, related := range violation.Related {
			if _, exists := seen[related]; !exists {
				seen[related] = struct{}{}
				result = append(result, related)
			}
		}
	}
	slices.SortFunc(result, func(left, right domain.ObjectRef) int {
		if order := strings.Compare(left.Kind, right.Kind); order != 0 {
			return order
		}
		return strings.Compare(left.ID, right.ID)
	})
	return result
}

func (engine *engine) report(
	ctx context.Context,
	phase ProgressPhase,
	objective domain.ObjectiveVector,
) error {
	if engine.sink == nil {
		return ctx.Err()
	}
	return engine.sink.Report(ctx, Progress{
		Phase:              phase,
		Evaluations:        engine.budget.Consumed(),
		Budget:             engine.budget.Limit(),
		BestObjective:      objective,
		FeasibleCandidates: engine.feasible,
		RejectedCandidates: engine.rejected,
	})
}

func (engine *engine) terminalResult(
	status SolveStatus,
	termination TerminationReason,
) (SolveResult, error) {
	engine.termination = termination
	evidence, err := engine.buildEvidence(status, []domain.ObjectRef{}, "")
	if err != nil {
		return SolveResult{}, err
	}
	return SolveResult{Status: status, Evidence: evidence}, nil
}

func (engine *engine) buildEvidence(
	status SolveStatus,
	conflicts []domain.ObjectRef,
	incumbentStateKey domain.ArtifactDigest,
) (SolveEvidence, error) {
	registryDigest, err := requiredOperatorRegistryDigest()
	if err != nil {
		return SolveEvidence{}, fmt.Errorf("digest operator registry: %w", err)
	}
	operatorEvaluations := make([]OperatorEvaluation, 0, len(requiredOperatorOrder))
	for _, operatorID := range requiredOperatorOrder {
		operatorEvaluations = append(operatorEvaluations, engine.operatorStats[operatorID])
	}
	noGoodDigest, err := engine.noGoods.Digest()
	if err != nil {
		return SolveEvidence{}, err
	}
	evidence := SolveEvidence{
		SchemaVersion:        SolveEvidenceVersion,
		ProblemDigest:        engine.problem.ProblemDigest,
		ConfigDigest:         engine.configDigest,
		Solver:               engine.identity,
		Status:               status,
		Termination:          engine.termination,
		BudgetLimit:          engine.budget.Limit(),
		Evaluations:          engine.budget.Consumed(),
		ConstructionAttempts: engine.constructionAttempts,
		ConstructionAccepted: engine.accepted,
		FeasibleCandidates:   engine.feasible,
		RejectedCandidates:   engine.rejected,
		RegistryDigest:       registryDigest,
		TerminalCursor:       engine.cursor,
		IncumbentStateKey:    incumbentStateKey,
		AcceptedStateKeys: append(
			[]domain.ArtifactDigest{},
			engine.acceptedStateKeys...,
		),
		LearnedNoGoods:      engine.noGoods.learned,
		AppliedNoGoods:      engine.noGoods.applied,
		NoGoodDigest:        noGoodDigest,
		RouteSeedDigest:     engine.config.RouteSeedDigest,
		OperatorEvaluations: operatorEvaluations,
		ConflictSet:         append([]domain.ObjectRef{}, conflicts...),
	}
	digest, err := ComputeEvidenceDigest(evidence)
	if err != nil {
		return SolveEvidence{}, fmt.Errorf("digest solve evidence: %w", err)
	}
	evidence.EvidenceDigest = digest
	return evidence, nil
}

func (engine *engine) abort(cause error) (SolveResult, error) {
	termination := TerminationCanceled
	if errors.Is(cause, context.DeadlineExceeded) {
		termination = TerminationDeadlineExceeded
	}
	result, err := engine.terminalResult(SolveAborted, termination)
	if err != nil {
		return SolveResult{}, errors.Join(cause, err)
	}
	return result, cause
}

func containsSkills(available, required domain.SkillSet) bool {
	for _, skill := range required {
		if !slices.Contains(available, skill) {
			return false
		}
	}
	return true
}

func (engine *engine) applyCandidate(
	item workItem,
	current candidate,
	duties map[domain.VehicleID]domain.VehicleDuty,
	driverVehicle map[domain.DriverID]domain.VehicleID,
	requestVehicle map[domain.RequestID]domain.VehicleID,
) {
	duty := duties[current.vehicle.ID]
	duty.VehicleID = current.vehicle.ID
	if !slices.Contains(duty.DriverIDs, current.driver.ID) {
		duty.DriverIDs = append(duty.DriverIDs, current.driver.ID)
		slices.Sort(duty.DriverIDs)
	}
	if current.replaceLast {
		duty.Trips[len(duty.Trips)-1] = current.trip
	} else {
		duty.Trips = append(duty.Trips, current.trip)
	}
	duties[current.vehicle.ID] = duty
	driverVehicle[current.driver.ID] = current.vehicle.ID
	if engine.index.requests[item.requestID].Split.SameVehicle {
		requestVehicle[item.requestID] = current.vehicle.ID
	}
	engine.acceptedStateKeys = append(
		engine.acceptedStateKeys,
		domain.ArtifactDigest(current.state.stateKey),
	)
}

func appendUnassigned(
	target *[]domain.UnassignedUnit,
	items []workItem,
	reason domain.UnassignedReason,
	detail string,
) {
	for _, item := range items {
		for _, unitID := range item.request.UnitIDs {
			*target = append(*target, domain.UnassignedUnit{
				UnitID: unitID,
				Reason: reason,
				Detail: detail,
			})
		}
	}
}

func constructionUnassigned(
	committed []domain.UnassignedUnit,
	pending []workItem,
	candidateUnits []domain.FulfillmentUnitID,
) []domain.UnassignedUnit {
	excluded := make(map[domain.FulfillmentUnitID]struct{}, len(candidateUnits))
	for _, unitID := range candidateUnits {
		excluded[unitID] = struct{}{}
	}
	result := append([]domain.UnassignedUnit{}, committed...)
	for _, item := range pending {
		for _, unitID := range item.request.UnitIDs {
			if _, skip := excluded[unitID]; skip {
				continue
			}
			result = append(result, domain.UnassignedUnit{
				UnitID: unitID,
				Reason: domain.UnassignedSearchExhausted,
				Detail: "pending deterministic construction",
			})
		}
	}
	return result
}

func (engine *engine) combineTripRequest(
	trip domain.Trip,
	added domain.TransportRequest,
) domain.TransportRequest {
	taskIDs := make(map[domain.TaskID]struct{})
	tasks := make([]domain.ServiceTask, 0)
	unitIDs := make([]domain.FulfillmentUnitID, 0)
	requiredSkills := append(domain.SkillSet(nil), added.RequiredSkills...)
	appendTask := func(task domain.ServiceTask) {
		if _, exists := taskIDs[task.ID]; exists {
			return
		}
		taskIDs[task.ID] = struct{}{}
		tasks = append(tasks, task)
		unitIDs = append(unitIDs, task.UnitIDs...)
		if requestID, exists := engine.index.taskRequests[task.ID]; exists {
			requiredSkills = append(
				requiredSkills,
				engine.index.requests[requestID].RequiredSkills...,
			)
		}
	}
	for _, stop := range trip.Stops {
		for _, taskID := range stop.TaskIDs {
			appendTask(engine.index.tasks[taskID])
		}
	}
	for _, task := range added.Tasks {
		appendTask(task)
	}
	slices.SortFunc(tasks, func(left, right domain.ServiceTask) int {
		return strings.Compare(string(left.ID), string(right.ID))
	})
	unitIDs = slices.Compact(slices.Sorted(slices.Values(unitIDs)))
	requiredSkills = slices.Compact(slices.Sorted(slices.Values(requiredSkills)))
	return domain.TransportRequest{
		ID:             added.ID,
		Priority:       added.Priority,
		Required:       true,
		Tasks:          tasks,
		UnitIDs:        unitIDs,
		Split:          domain.SplitPolicy{Mode: domain.SplitForbidden, MaxSplits: 1},
		RequiredSkills: requiredSkills,
	}
}
