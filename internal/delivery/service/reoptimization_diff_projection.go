package service

import (
	"reflect"
	"slices"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type cargoPlacementSnapshot struct {
	placement  domain.Placement
	stopIndex  uint32
	stageIndex uint32
}

func planCargoPlacements(plan domain.Plan) map[domain.CargoID]cargoPlacementSnapshot {
	result := make(map[domain.CargoID]cargoPlacementSnapshot)
	visits := taskVisits(plan)
	for _, duty := range plan.Duties {
		for _, trip := range duty.Trips {
			for _, stage := range trip.LoadStages {
				for _, placement := range stage.Placements {
					stopIndex := stage.AfterStopIndex
					if unload, exists := visits[placement.UnloadAtTaskID]; exists {
						stopIndex = unload.index
					}
					current, exists := result[placement.CargoID]
					if !exists || stage.AfterStopIndex < current.stageIndex {
						result[placement.CargoID] = cargoPlacementSnapshot{
							placement:  placement,
							stopIndex:  stopIndex,
							stageIndex: stage.AfterStopIndex,
						}
					}
				}
			}
		}
	}
	return result
}

type Agent3ComparisonProjection interface{ isAgent3ComparisonProjection() }
type Agent3ComparisonUnavailable struct {
	Kind string `json:"kind"`
}

func (Agent3ComparisonUnavailable) isAgent3ComparisonProjection() {
}

type Agent3ComparisonAvailable struct {
	Kind                string                   `json:"kind"`
	BaseRevisionID      domain.PlanRevisionID    `json:"base_revision_id"`
	ChangedVehicleCount uint32                   `json:"changed_vehicle_count"`
	ChangedDriverCount  uint32                   `json:"changed_driver_count"`
	ReorderedStopCount  uint32                   `json:"reordered_stop_count"`
	ETADriftSeconds     int64                    `json:"eta_drift_seconds"`
	ReloadedCargoCount  uint32                   `json:"reloaded_cargo_count"`
	StabilityCostCents  int64                    `json:"stability_cost_cents"`
	Changes             []Agent3ComparisonChange `json:"changes"`
}

func (Agent3ComparisonAvailable) isAgent3ComparisonProjection() {
}

type Agent3ComparisonChange interface{ isAgent3ComparisonChange() }
type Agent3Assignment struct {
	Kind      string           `json:"kind"`
	VehicleID domain.VehicleID `json:"vehicle_id,omitempty"`
}

type Agent3VehicleAssignmentChange struct {
	Kind   string                   `json:"kind"`
	UnitID domain.FulfillmentUnitID `json:"unit_id"`
	Before Agent3Assignment         `json:"before"`
	After  Agent3Assignment         `json:"after"`
}

func (Agent3VehicleAssignmentChange) isAgent3ComparisonChange() {
}

type Agent3DriverAssignmentChange struct {
	Kind            string            `json:"kind"`
	VehicleID       domain.VehicleID  `json:"vehicle_id"`
	BeforeDriverIDs []domain.DriverID `json:"before_driver_ids"`
	AfterDriverIDs  []domain.DriverID `json:"after_driver_ids"`
}

func (Agent3DriverAssignmentChange) isAgent3ComparisonChange() {
}

type Agent3StopSequenceChange struct {
	Kind        string            `json:"kind"`
	VehicleID   domain.VehicleID  `json:"vehicle_id"`
	TripID      domain.TripID     `json:"trip_id"`
	LocationID  domain.LocationID `json:"location_id"`
	BeforeIndex uint32            `json:"before_index"`
	AfterIndex  uint32            `json:"after_index"`
}

func (Agent3StopSequenceChange) isAgent3ComparisonChange() {
}

type Agent3ETAChange struct {
	Kind         string        `json:"kind"`
	TaskID       domain.TaskID `json:"task_id"`
	BeforeAt     time.Time     `json:"before_at"`
	AfterAt      time.Time     `json:"after_at"`
	DriftSeconds int64         `json:"drift_seconds"`
}

func (Agent3ETAChange) isAgent3ComparisonChange() {
}

type Agent3CargoPlacementChange struct {
	Kind            string         `json:"kind"`
	CargoID         domain.CargoID `json:"cargo_id"`
	BeforeStopIndex uint32         `json:"before_stop_index"`
	AfterStopIndex  uint32         `json:"after_stop_index"`
	BeforeDoorID    domain.DoorID  `json:"before_door_id"`
	AfterDoorID     domain.DoorID  `json:"after_door_id"`
}

func (Agent3CargoPlacementChange) isAgent3ComparisonChange() {
}

type Agent3MetricChange struct {
	Kind   string `json:"kind"`
	Metric string `json:"metric"`
	Before int64  `json:"before"`
	After  int64  `json:"after"`
	Delta  int64  `json:"delta"`
}

func (Agent3MetricChange) isAgent3ComparisonChange() {
}

func buildAgent3Projection(
	baseProblem domain.ProblemSnapshot,
	candidateProblem domain.ProblemSnapshot,
	base domain.Plan,
	candidate domain.Plan,
) Agent3ComparisonProjection {
	changes := make([]Agent3ComparisonChange, 0)
	baseVisits := taskVisits(base)
	candidateVisits := taskVisits(candidate)
	unitIDs := make(map[domain.FulfillmentUnitID]struct{})
	baseAssignments := planUnitAssignments(baseProblem, base, unitIDs)
	candidateAssignments := planUnitAssignments(candidateProblem, candidate, unitIDs)
	for unitID := range unitIDs {
		if _, exists := baseAssignments[unitID]; !exists {
			baseAssignments[unitID] = Agent3Assignment{Kind: "unassigned"}
		}
		if _, exists := candidateAssignments[unitID]; !exists {
			candidateAssignments[unitID] = Agent3Assignment{Kind: "unassigned"}
		}
	}
	changedVehicles := make(map[domain.VehicleID]struct{})
	for _, unitID := range sortedUnitIDs(unitIDs) {
		before := baseAssignments[unitID]
		after := candidateAssignments[unitID]
		if before == after {
			continue
		}
		changes = append(
			changes,
			Agent3VehicleAssignmentChange{
				Kind:   "vehicle_assignment",
				UnitID: unitID,
				Before: before,
				After:  after,
			},
		)
		if before.VehicleID != "" {
			changedVehicles[before.VehicleID] = struct{}{}
		}
		if after.VehicleID != "" {
			changedVehicles[after.VehicleID] = struct{}{}
		}
	}
	baseDrivers := dutyDrivers(base)
	candidateDrivers := dutyDrivers(candidate)
	vehicleIDs := unionVehicleIDs(baseDrivers, candidateDrivers)
	var changedDriverCount uint32
	for _, vehicleID := range vehicleIDs {
		before := baseDrivers[vehicleID]
		after := candidateDrivers[vehicleID]
		if len(before) == 0 || len(after) == 0 || slices.Equal(before, after) {
			continue
		}
		changedDriverCount++
		changes = append(
			changes,
			Agent3DriverAssignmentChange{
				Kind:            "driver_assignment",
				VehicleID:       vehicleID,
				BeforeDriverIDs: before,
				AfterDriverIDs:  after,
			},
		)
	}
	taskIDs := unionTaskIDs(baseVisits, candidateVisits)
	var reordered uint32
	var totalDrift int64
	for _, taskID := range taskIDs {
		before, beforeExists := baseVisits[taskID]
		after, afterExists := candidateVisits[taskID]
		if !beforeExists || !afterExists {
			continue
		}
		if before.vehicleID == after.vehicleID && before.tripID == after.tripID &&
			before.index != after.index {
			reordered++
			changes = append(
				changes,
				Agent3StopSequenceChange{
					Kind:        "stop_sequence",
					VehicleID:   after.vehicleID,
					TripID:      after.tripID,
					LocationID:  after.location,
					BeforeIndex: before.index,
					AfterIndex:  after.index,
				},
			)
		}
		if !before.serviceAt.Equal(after.serviceAt) {
			drift := int64(after.serviceAt.Sub(before.serviceAt) / time.Second)
			totalDrift += drift
			changes = append(
				changes,
				Agent3ETAChange{
					Kind:         "eta",
					TaskID:       taskID,
					BeforeAt:     before.serviceAt,
					AfterAt:      after.serviceAt,
					DriftSeconds: drift,
				},
			)
		}
	}
	baseCargo := planCargoPlacements(base)
	candidateCargo := planCargoPlacements(candidate)
	cargoIDs := unionCargoIDs(baseCargo, candidateCargo)
	var reloaded uint32
	for _, cargoID := range cargoIDs {
		before, beforeExists := baseCargo[cargoID]
		after, afterExists := candidateCargo[cargoID]
		if !beforeExists || !afterExists ||
			(before.stopIndex == after.stopIndex && before.placement.DoorID == after.placement.DoorID) {
			continue
		}
		reloaded++
		changes = append(
			changes,
			Agent3CargoPlacementChange{
				Kind:            "cargo_placement",
				CargoID:         cargoID,
				BeforeStopIndex: before.stopIndex,
				AfterStopIndex:  after.stopIndex,
				BeforeDoorID:    before.placement.DoorID,
				AfterDoorID:     after.placement.DoorID,
			},
		)
	}
	for _, metric := range agent3MetricRegistry() {
		before := metric.value(base.Metrics)
		after := metric.value(candidate.Metrics)
		if before == after {
			continue
		}
		changes = append(
			changes,
			Agent3MetricChange{
				Kind:   "metric",
				Metric: metric.name,
				Before: before,
				After:  after,
				Delta:  after - before,
			},
		)
	}
	if len(changes) == 0 {
		return Agent3ComparisonUnavailable{Kind: "unavailable"}
	}
	return Agent3ComparisonAvailable{
		Kind:                "available",
		BaseRevisionID:      base.RevisionID,
		ChangedVehicleCount: uint32(len(changedVehicles)),
		ChangedDriverCount:  changedDriverCount,
		ReorderedStopCount:  reordered,
		ETADriftSeconds:     totalDrift,
		ReloadedCargoCount:  reloaded,
		StabilityCostCents:  candidate.Metrics.StabilityCostCents,
		Changes:             changes,
	}
}

func planUnitAssignments(
	problem domain.ProblemSnapshot,
	plan domain.Plan,
	unitIDs map[domain.FulfillmentUnitID]struct{},
) map[domain.FulfillmentUnitID]Agent3Assignment {
	result := make(map[domain.FulfillmentUnitID]Agent3Assignment)
	visits := taskVisits(plan)
	for _, request := range problem.Requests {
		for _, unitID := range request.UnitIDs {
			unitIDs[unitID] = struct{}{}
			result[unitID] = unitAssignment(request, unitID, visits)
		}
	}
	for _, unassigned := range plan.Unassigned {
		unitIDs[unassigned.UnitID] = struct{}{}
		result[unassigned.UnitID] = Agent3Assignment{Kind: "unassigned"}
	}
	return result
}

func sortedUnitIDs(values map[domain.FulfillmentUnitID]struct{}) []domain.FulfillmentUnitID {
	result := make([]domain.FulfillmentUnitID, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	slices.Sort(result)
	return result
}

func dutyDrivers(plan domain.Plan) map[domain.VehicleID][]domain.DriverID {
	result := make(map[domain.VehicleID][]domain.DriverID)
	for _, duty := range plan.Duties {
		drivers := append([]domain.DriverID(nil), duty.DriverIDs...)
		slices.Sort(drivers)
		result[duty.VehicleID] = drivers
	}
	return result
}

func unionVehicleIDs(
	left map[domain.VehicleID][]domain.DriverID,
	right map[domain.VehicleID][]domain.DriverID,
) []domain.VehicleID {
	values := make(map[domain.VehicleID]struct{}, len(left)+len(right))
	for value := range left {
		values[value] = struct{}{}
	}
	for value := range right {
		values[value] = struct{}{}
	}
	result := make([]domain.VehicleID, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	slices.Sort(result)
	return result
}

func unionTaskIDs(
	left map[domain.TaskID]taskVisitSnapshot,
	right map[domain.TaskID]taskVisitSnapshot,
) []domain.TaskID {
	values := make(map[domain.TaskID]struct{}, len(left)+len(right))
	for value := range left {
		values[value] = struct{}{}
	}
	for value := range right {
		values[value] = struct{}{}
	}
	result := make([]domain.TaskID, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	slices.Sort(result)
	return result
}

func unionCargoIDs(
	left map[domain.CargoID]cargoPlacementSnapshot,
	right map[domain.CargoID]cargoPlacementSnapshot,
) []domain.CargoID {
	values := make(map[domain.CargoID]struct{}, len(left)+len(right))
	for value := range left {
		values[value] = struct{}{}
	}
	for value := range right {
		values[value] = struct{}{}
	}
	result := make([]domain.CargoID, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	slices.Sort(result)
	return result
}

func agent3MetricRegistry() []metricDescriptor {
	wanted := map[string]struct{}{
		"vehicles_used":               {},
		"total_distance_meters":       {},
		"total_cost_cents":            {},
		"on_time_rate_ppm":            {},
		"mean_volume_utilization_ppm": {},
		"stability_cost_cents":        {},
	}
	result := make([]metricDescriptor, 0, len(wanted))
	for _, metric := range planMetricRegistry {
		if _, exists := wanted[metric.name]; exists {
			result = append(result, metric)
		}
	}
	return result
}

func planMetricRegistryFieldCount() int {
	return reflect.TypeOf(domain.PlanMetrics{}).NumField()
}

func comparisonSequenceShift(before uint32, after int) uint32 {
	if after < 0 {
		return ^uint32(0)
	}
	current := uint64(after)
	expected := uint64(before)
	if current >= expected {
		difference := current - expected
		if difference > uint64(^uint32(0)) {
			return ^uint32(0)
		}
		return uint32(difference)
	}
	return uint32(expected - current)
}
