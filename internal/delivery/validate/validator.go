package validate

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type Validator struct {
	identity domain.ValidatorIdentity
}

type taskDefinition struct {
	request domain.TransportRequest
	task    domain.ServiceTask
}

type taskVisit struct {
	dutyIndex int
	tripIndex int
	stopIndex int
	vehicleID domain.VehicleID
	driverIDs []domain.DriverID
	stop      domain.Stop
}

type validationState struct {
	problem domain.ProblemSnapshot
	plan    domain.Plan

	locations      map[domain.LocationID]domain.Location
	depots         map[domain.DepotID]domain.Depot
	requests       map[domain.RequestID]domain.TransportRequest
	tasks          map[domain.TaskID]taskDefinition
	units          map[domain.FulfillmentUnitID]domain.FulfillmentUnit
	cargo          map[domain.CargoID]domain.CargoItem
	vehicles       map[domain.VehicleID]domain.Vehicle
	drivers        map[domain.DriverID]domain.Driver
	chargers       map[domain.ChargerID]domain.ChargingStation
	travelIndex    map[domain.LocationID]int
	energyIndex    map[domain.LocationID]int
	energyProfiles map[string]domain.EnergyProfileMatrix
	visits         map[domain.TaskID][]taskVisit

	violations []domain.Violation
	metrics    domain.PlanMetrics
}

func New(identity domain.ValidatorIdentity) Validator {
	return Validator{identity: identity}
}

func (validator Validator) Validate(
	problem domain.ProblemSnapshot,
	plan domain.Plan,
	createdAt time.Time,
) domain.ValidationReport {
	state := newValidationState(problem, plan)
	state.validateBindings()
	state.validateOrderConservation()
	state.validatePickupDelivery()
	state.validateResources()
	state.validateRoutes()
	state.validateSchedules()
	state.validateEnergy()
	state.validateGeometry()
	state.validateSupport()
	state.validateExtraction()
	state.validateVehicleLoads()
	state.validateCommitments()
	state.validateMetrics()

	slices.SortFunc(state.violations, compareViolations)
	valid := true
	for _, violation := range state.violations {
		if violation.Severity == domain.SeverityError {
			valid = false
			break
		}
	}
	report := domain.ValidationReport{
		SchemaVersion:    domain.ValidationSchemaVersion,
		ProblemDigest:    problem.ProblemDigest,
		PolicyDigest:     problem.PolicyDigest,
		CommitmentDigest: problem.CommitmentDigest,
		PlanDigest:       plan.PlanDigest,
		Validator:        validator.identity,
		Valid:            valid,
		Violations:       state.violations,
		Metrics:          state.metrics,
		CreatedAt:        createdAt.UTC(),
	}
	digest, err := domain.ComputeReportDigest(report)
	if err != nil {
		state.add("V004", domain.SeverityError, "validation_report", ref(""),
			"canonical report", err.Error(), position{})
		report.Valid = false
		report.Violations = state.violations
		return report
	}
	report.ReportDigest = digest
	return report
}

func newValidationState(
	problem domain.ProblemSnapshot,
	plan domain.Plan,
) *validationState {
	state := &validationState{
		problem:        problem,
		plan:           plan,
		locations:      make(map[domain.LocationID]domain.Location, len(problem.Locations)),
		depots:         make(map[domain.DepotID]domain.Depot, len(problem.Depots)),
		requests:       make(map[domain.RequestID]domain.TransportRequest, len(problem.Requests)),
		tasks:          make(map[domain.TaskID]taskDefinition),
		units:          make(map[domain.FulfillmentUnitID]domain.FulfillmentUnit, len(problem.Units)),
		cargo:          make(map[domain.CargoID]domain.CargoItem, len(problem.Cargo)),
		vehicles:       make(map[domain.VehicleID]domain.Vehicle, len(problem.Vehicles)),
		drivers:        make(map[domain.DriverID]domain.Driver, len(problem.Drivers)),
		chargers:       make(map[domain.ChargerID]domain.ChargingStation, len(problem.Chargers)),
		travelIndex:    make(map[domain.LocationID]int, len(problem.Travel.NodeIDs)),
		energyIndex:    make(map[domain.LocationID]int, len(problem.Energy.NodeIDs)),
		energyProfiles: make(map[string]domain.EnergyProfileMatrix, len(problem.Energy.Profiles)),
		visits:         make(map[domain.TaskID][]taskVisit),
		violations:     make([]domain.Violation, 0),
	}
	for _, value := range problem.Locations {
		state.locations[value.ID] = value
	}
	for _, value := range problem.Depots {
		state.depots[value.ID] = value
	}
	for _, request := range problem.Requests {
		state.requests[request.ID] = request
		for _, task := range request.Tasks {
			state.tasks[task.ID] = taskDefinition{request: request, task: task}
		}
	}
	for _, value := range problem.Units {
		state.units[value.ID] = value
	}
	for _, value := range problem.Cargo {
		state.cargo[value.ID] = value
	}
	for _, value := range problem.Vehicles {
		state.vehicles[value.ID] = value
	}
	for _, value := range problem.Drivers {
		state.drivers[value.ID] = value
	}
	for _, value := range problem.Chargers {
		state.chargers[value.ID] = value
	}
	for index, value := range problem.Travel.NodeIDs {
		state.travelIndex[value] = index
	}
	for index, value := range problem.Energy.NodeIDs {
		state.energyIndex[value] = index
	}
	for _, value := range problem.Energy.Profiles {
		state.energyProfiles[value.ProfileID] = value
	}
	for dutyIndex, duty := range plan.Duties {
		for tripIndex, trip := range duty.Trips {
			for stopIndex, stop := range trip.Stops {
				for _, taskID := range stop.TaskIDs {
					state.visits[taskID] = append(state.visits[taskID], taskVisit{
						dutyIndex: dutyIndex,
						tripIndex: tripIndex,
						stopIndex: stopIndex,
						vehicleID: duty.VehicleID,
						driverIDs: append([]domain.DriverID(nil), duty.DriverIDs...),
						stop:      stop,
					})
				}
			}
		}
	}
	return state
}

type position struct {
	duty    int
	trip    int
	stop    int
	segment int
}

func (state *validationState) add(
	code string,
	severity domain.Severity,
	kind string,
	id fmt.Stringer,
	expected string,
	actual string,
	at position,
	related ...domain.ObjectRef,
) {
	objectID := ""
	if id != nil {
		objectID = id.String()
	}
	state.violations = append(state.violations, domain.Violation{
		Code:         code,
		Severity:     severity,
		Object:       domain.ObjectRef{Kind: kind, ID: objectID},
		Related:      append([]domain.ObjectRef(nil), related...),
		Expected:     expected,
		Actual:       actual,
		DutyIndex:    int32(at.duty),
		TripIndex:    int32(at.trip),
		StopIndex:    int32(at.stop),
		SegmentIndex: int32(at.segment),
	})
}

type stringRef string

func (value stringRef) String() string {
	return string(value)
}

func ref[T ~string](value T) fmt.Stringer {
	return stringRef(value)
}

func noPosition() position {
	return position{duty: -1, trip: -1, stop: -1, segment: -1}
}

func atStop(duty, trip, stop int) position {
	return position{duty: duty, trip: trip, stop: stop, segment: -1}
}

func atSegment(duty, trip, segment int) position {
	return position{duty: duty, trip: trip, stop: -1, segment: segment}
}

func compareViolations(left, right domain.Violation) int {
	if result := strings.Compare(left.Code, right.Code); result != 0 {
		return result
	}
	if result := strings.Compare(left.Object.Kind, right.Object.Kind); result != 0 {
		return result
	}
	if result := strings.Compare(left.Object.ID, right.Object.ID); result != 0 {
		return result
	}
	if left.DutyIndex != right.DutyIndex {
		return int(left.DutyIndex - right.DutyIndex)
	}
	if left.TripIndex != right.TripIndex {
		return int(left.TripIndex - right.TripIndex)
	}
	if left.StopIndex != right.StopIndex {
		return int(left.StopIndex - right.StopIndex)
	}
	return int(left.SegmentIndex - right.SegmentIndex)
}

func (state *validationState) matrixValue(
	values []int64,
	from domain.LocationID,
	to domain.LocationID,
) (int64, bool) {
	fromIndex, fromExists := state.travelIndex[from]
	toIndex, toExists := state.travelIndex[to]
	size := len(state.problem.Travel.NodeIDs)
	if !fromExists || !toExists || len(values) != size*size {
		return 0, false
	}
	return values[fromIndex*size+toIndex], true
}

func durationSeconds(start, end time.Time) int64 {
	if end.Before(start) {
		return -1
	}
	return int64(end.Sub(start) / time.Second)
}

func formatInt(value int64) string {
	return strconv.FormatInt(value, 10)
}

func containsAllSkills(have, required domain.SkillSet) bool {
	for _, skill := range required {
		if !slices.Contains(have, skill) {
			return false
		}
	}
	return true
}
