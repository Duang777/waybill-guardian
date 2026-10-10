package solve

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type stateKey domain.ArtifactDigest

type tripComponentKey struct {
	VehicleID domain.VehicleID
	TripIndex uint32
}

type componentIndex struct {
	dutyInputDigests  map[domain.VehicleID]domain.ArtifactDigest
	dutyOutputDigests map[domain.VehicleID]domain.ArtifactDigest
	tripInputDigests  map[tripComponentKey]domain.ArtifactDigest
	tripOutputDigests map[tripComponentKey]domain.ArtifactDigest
}

type dependencyEdges uint8

const (
	dependencyPreviousEndTime dependencyEdges = 1 << iota
	dependencyPreviousEndDepot
	dependencyPreviousEndSOC

	allDependencyEdges = dependencyPreviousEndTime |
		dependencyPreviousEndDepot |
		dependencyPreviousEndSOC
)

type breakDirective struct {
	BeforeStopIndex uint32 `json:"before_stop_index"`
	DurationSeconds int64  `json:"duration_seconds"`
}

type chargerDirective struct {
	FromStopIndex uint32           `json:"from_stop_index"`
	ChargerID     domain.ChargerID `json:"charger_id"`
}

type placementDecision struct {
	CargoID       domain.CargoID       `json:"cargo_id"`
	CompartmentID domain.CompartmentID `json:"compartment_id"`
	PositionMM    domain.Point3        `json:"position_mm"`
	Orientation   domain.Orientation   `json:"orientation"`
	DoorID        domain.DoorID        `json:"door_id"`
}

type loadStageDecision struct {
	AfterStopIndex uint32              `json:"after_stop_index"`
	Placements     []placementDecision `json:"placements"`
}

type tripControls struct {
	Breaks     []breakDirective    `json:"breaks"`
	Chargers   []chargerDirective  `json:"chargers"`
	LoadStages []loadStageDecision `json:"load_stages"`
}

type tripDecision struct {
	ID           domain.TripID   `json:"id"`
	StartDepotID domain.DepotID  `json:"start_depot_id"`
	EndDepotID   domain.DepotID  `json:"end_depot_id"`
	DriverID     domain.DriverID `json:"driver_id"`
	TaskIDs      []domain.TaskID `json:"task_ids"`
	Controls     tripControls    `json:"controls"`
}

type previousTripDependency struct {
	Present    bool           `json:"present"`
	EndAt      time.Time      `json:"end_at,omitempty"`
	EndDepotID domain.DepotID `json:"end_depot_id,omitempty"`
	EndSOCWh   int64          `json:"end_soc_wh,omitempty"`
}

type tripInputDigestBody struct {
	ProblemDigest domain.ArtifactDigest  `json:"problem_digest"`
	VehicleID     domain.VehicleID       `json:"vehicle_id"`
	DutyDriverIDs []domain.DriverID      `json:"duty_driver_ids"`
	Decision      tripDecision           `json:"decision"`
	Previous      previousTripDependency `json:"previous"`
}

type dutyInputDigestBody struct {
	ProblemDigest    domain.ArtifactDigest   `json:"problem_digest"`
	VehicleID        domain.VehicleID        `json:"vehicle_id"`
	DriverIDs        []domain.DriverID       `json:"driver_ids"`
	TripInputDigests []domain.ArtifactDigest `json:"trip_input_digests"`
}

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

type candidateStateBody struct {
	Duties     []domain.VehicleDuty    `json:"duties"`
	Unassigned []domain.UnassignedUnit `json:"unassigned"`
	Objective  domain.ObjectiveVector  `json:"objective"`
	Metrics    domain.PlanMetrics      `json:"metrics"`
}

func candidateStateFromPlan(plan domain.Plan) (candidateState, error) {
	normalized := normalizeCandidatePlan(plan)
	body := candidateStateBody{
		Duties:     normalized.Duties,
		Unassigned: normalized.Unassigned,
		Objective:  normalized.Objective,
		Metrics:    normalized.Metrics,
	}
	digest, err := domain.Digest(body)
	if err != nil {
		return candidateState{}, fmt.Errorf("digest candidate state: %w", err)
	}
	components, err := indexCandidateComponents(normalized)
	if err != nil {
		return candidateState{}, err
	}
	return candidateState{
		plan:       normalized,
		objective:  normalized.Objective,
		stateKey:   stateKey(digest),
		components: components,
	}, nil
}

func indexCandidateComponents(plan domain.Plan) (componentIndex, error) {
	return indexCandidateComponentsWithDependencies(plan, allDependencyEdges)
}

func indexCandidateComponentsWithDependencies(
	plan domain.Plan,
	edges dependencyEdges,
) (componentIndex, error) {
	index := componentIndex{
		dutyInputDigests:  make(map[domain.VehicleID]domain.ArtifactDigest, len(plan.Duties)),
		dutyOutputDigests: make(map[domain.VehicleID]domain.ArtifactDigest, len(plan.Duties)),
		tripInputDigests:  make(map[tripComponentKey]domain.ArtifactDigest),
		tripOutputDigests: make(map[tripComponentKey]domain.ArtifactDigest),
	}
	for _, duty := range plan.Duties {
		digest, err := domain.Digest(duty)
		if err != nil {
			return componentIndex{}, fmt.Errorf(
				"digest duty %q component: %w",
				duty.VehicleID,
				err,
			)
		}
		index.dutyOutputDigests[duty.VehicleID] = digest
		tripInputs := make([]domain.ArtifactDigest, 0, len(duty.Trips))
		for tripIndex, trip := range duty.Trips {
			decision, decisionErr := materializedTripDecision(duty, trip)
			if decisionErr != nil {
				return componentIndex{}, fmt.Errorf(
					"duty %q trip %d decision: %w",
					duty.VehicleID,
					tripIndex,
					decisionErr,
				)
			}
			inputDigest, inputErr := digestTripInput(
				plan.ProblemDigest,
				duty,
				decision,
				previousDependency(duty.Trips, tripIndex, edges),
			)
			if inputErr != nil {
				return componentIndex{}, fmt.Errorf(
					"digest duty %q trip %d input: %w",
					duty.VehicleID,
					tripIndex,
					inputErr,
				)
			}
			digest, err = domain.Digest(trip)
			if err != nil {
				return componentIndex{}, fmt.Errorf(
					"digest duty %q trip %d component: %w",
					duty.VehicleID,
					tripIndex,
					err,
				)
			}
			key := tripComponentKey{
				VehicleID: duty.VehicleID,
				TripIndex: uint32(tripIndex),
			}
			index.tripInputDigests[key] = inputDigest
			index.tripOutputDigests[key] = digest
			tripInputs = append(tripInputs, inputDigest)
		}
		dutyInput, err := domain.Digest(dutyInputDigestBody{
			ProblemDigest:    plan.ProblemDigest,
			VehicleID:        duty.VehicleID,
			DriverIDs:        append([]domain.DriverID{}, duty.DriverIDs...),
			TripInputDigests: tripInputs,
		})
		if err != nil {
			return componentIndex{}, fmt.Errorf(
				"digest duty %q input: %w",
				duty.VehicleID,
				err,
			)
		}
		index.dutyInputDigests[duty.VehicleID] = dutyInput
	}
	return index, nil
}

func digestTripInput(
	problemDigest domain.ArtifactDigest,
	duty domain.VehicleDuty,
	decision tripDecision,
	previous previousTripDependency,
) (domain.ArtifactDigest, error) {
	return domain.Digest(tripInputDigestBody{
		ProblemDigest: problemDigest,
		VehicleID:     duty.VehicleID,
		DutyDriverIDs: append([]domain.DriverID{}, duty.DriverIDs...),
		Decision:      decision,
		Previous:      previous,
	})
}

func previousDependency(
	trips []domain.Trip,
	tripIndex int,
	edges dependencyEdges,
) previousTripDependency {
	if tripIndex == 0 {
		return previousTripDependency{}
	}
	previous := trips[tripIndex-1]
	result := previousTripDependency{Present: true}
	if edges&dependencyPreviousEndTime != 0 {
		result.EndAt = previous.EndAt.UTC()
	}
	if edges&dependencyPreviousEndDepot != 0 {
		result.EndDepotID = previous.EndDepotID
	}
	if edges&dependencyPreviousEndSOC != 0 && len(previous.Energy) > 0 {
		result.EndSOCWh = previous.Energy[len(previous.Energy)-1].EndSOCWh
	}
	return result
}

func normalizeCandidatePlan(plan domain.Plan) domain.Plan {
	plan.Duties = normalizeCandidateDuties(plan.Duties)
	plan.Unassigned = append([]domain.UnassignedUnit{}, plan.Unassigned...)
	slices.SortFunc(plan.Unassigned, func(left, right domain.UnassignedUnit) int {
		if result := strings.Compare(string(left.UnitID), string(right.UnitID)); result != 0 {
			return result
		}
		if result := strings.Compare(string(left.Reason), string(right.Reason)); result != 0 {
			return result
		}
		return strings.Compare(left.Detail, right.Detail)
	})
	return plan
}

func normalizeCandidateDuties(values []domain.VehicleDuty) []domain.VehicleDuty {
	result := append([]domain.VehicleDuty{}, values...)
	for dutyIndex := range result {
		duty := &result[dutyIndex]
		duty.DriverIDs = append([]domain.DriverID{}, duty.DriverIDs...)
		slices.Sort(duty.DriverIDs)
		duty.DriverIDs = slices.Compact(duty.DriverIDs)
		duty.Trips = append([]domain.Trip{}, duty.Trips...)
		for tripIndex := range duty.Trips {
			normalizeCandidateTrip(&duty.Trips[tripIndex])
		}
	}
	slices.SortFunc(result, func(left, right domain.VehicleDuty) int {
		return strings.Compare(string(left.VehicleID), string(right.VehicleID))
	})
	return result
}

func normalizeCandidateTrip(trip *domain.Trip) {
	trip.StartAt = trip.StartAt.UTC()
	trip.EndAt = trip.EndAt.UTC()
	trip.Stops = append([]domain.Stop{}, trip.Stops...)
	for stopIndex := range trip.Stops {
		stop := &trip.Stops[stopIndex]
		stop.TaskIDs = append([]domain.TaskID{}, stop.TaskIDs...)
		stop.ArrivalAt = stop.ArrivalAt.UTC()
		stop.ServiceAt = stop.ServiceAt.UTC()
		stop.DepartureAt = stop.DepartureAt.UTC()
	}
	trip.Schedule = append([]domain.DutySegment{}, trip.Schedule...)
	for segmentIndex := range trip.Schedule {
		segment := &trip.Schedule[segmentIndex]
		segment.TaskIDs = append([]domain.TaskID{}, segment.TaskIDs...)
		segment.StartAt = segment.StartAt.UTC()
		segment.EndAt = segment.EndAt.UTC()
	}
	trip.Energy = append([]domain.EnergyLeg{}, trip.Energy...)
	trip.LoadStages = append([]domain.LoadStage{}, trip.LoadStages...)
	for stageIndex := range trip.LoadStages {
		stage := &trip.LoadStages[stageIndex]
		stage.Placements = append([]domain.Placement{}, stage.Placements...)
		slices.SortFunc(stage.Placements, func(left, right domain.Placement) int {
			return strings.Compare(string(left.CargoID), string(right.CargoID))
		})
		stage.AxleLoadsG = append([]int64{}, stage.AxleLoadsG...)
		stage.Rehandles = append([]domain.RehandleOperation{}, stage.Rehandles...)
		slices.SortFunc(stage.Rehandles, func(
			left domain.RehandleOperation,
			right domain.RehandleOperation,
		) int {
			if left.Sequence < right.Sequence {
				return -1
			}
			if left.Sequence > right.Sequence {
				return 1
			}
			return strings.Compare(string(left.CargoID), string(right.CargoID))
		})
	}
}
