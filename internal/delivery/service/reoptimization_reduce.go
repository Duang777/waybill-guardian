package service

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type reducerContext struct {
	snapshot     *domain.ProblemSnapshot
	active       domain.Plan
	createdAt    time.Time
	protected    protectedExecutionClosure
	guardian     map[domain.TaskID]domain.FrozenTaskCommitment
	projectedETA map[domain.TaskID]time.Time
	claims       map[string]terminalClaim
	canceled     map[domain.RequestID]struct{}
	unloaded     map[domain.CargoID]struct{}
}

type terminalClaim struct {
	stream domain.FactStream
	digest domain.ArtifactDigest
	fact   domain.FactRef
}

func rejectCrossFactTerminalConflicts(
	base domain.ProblemSnapshot,
	facts []domain.LedgerFact,
) error {
	taskRequest := make(map[domain.TaskID]domain.RequestID)
	for _, request := range base.Requests {
		for _, task := range request.Tasks {
			taskRequest[task.ID] = request.ID
		}
	}
	cancellations := make(map[domain.RequestID]domain.FactRef)
	completions := make(map[domain.RequestID]domain.FactRef)
	newRequests := make(map[domain.RequestID]domain.FactRef)
	for _, fact := range facts {
		switch value := fact.Fact.(type) {
		case domain.NewRequestFact:
			if existing, duplicate := newRequests[value.Request.ID]; duplicate {
				return &SemanticFactConflictError{Key: "request/" + string(value.Request.ID) + "/definition", Existing: existing, Incoming: fact.Ref()}
			}
			newRequests[value.Request.ID] = fact.Ref()
		case domain.RequestCanceledFact:
			cancellations[value.RequestID] = fact.Ref()
		case domain.TaskCompletedFact:
			if requestID, exists := taskRequest[value.TaskID]; exists {
				completions[requestID] = fact.Ref()
			}
		}
	}
	for requestID, cancellation := range cancellations {
		if created, exists := newRequests[requestID]; exists {
			return &SemanticFactConflictError{
				Key:      "request/" + string(requestID) + "/terminal_state",
				Existing: created,
				Incoming: cancellation,
			}
		}
		if completion, exists := completions[requestID]; exists {
			return &SemanticFactConflictError{
				Key:      "request/" + string(requestID) + "/terminal_state",
				Existing: completion,
				Incoming: cancellation,
			}
		}
	}
	return nil
}

func applyOperationalFact(
	context *reducerContext,
	ledgerFact domain.LedgerFact,
) (domain.FactApplication, error) {
	ref := ledgerFact.Ref()
	application := domain.FactApplication{
		Fact:       ref,
		Objects:    []domain.ObjectRef{},
		FieldPaths: []string{},
	}
	add := func(kind, id string, paths ...string) {
		application.Objects = append(application.Objects, domain.ObjectRef{Kind: kind, ID: id})
		application.FieldPaths = append(application.FieldPaths, paths...)
	}
	switch value := ledgerFact.Fact.(type) {
	case domain.StreamOpenedFact:
		stream := value.Meta.Position.Stream
		add("fact_stream", strings.Join([]string{stream.SourceSystem, stream.Name, stream.Partition}, "/"), "/stream_epoch")
	case domain.NewRequestFact:
		if strings.TrimSpace(value.SourceRef.System) == "" || strings.TrimSpace(value.SourceRef.ResourceType) == "" || strings.TrimSpace(value.SourceRef.ResourceID) == "" || strings.TrimSpace(value.SourceRef.Version) == "" || value.SourceRef.ObservedAt.IsZero() || value.SourceRef.ObservedAt.After(ledgerFact.Fact.FactHeader().ObservedAt) {
			return domain.FactApplication{}, fmt.Errorf("new request fact source_ref is invalid")
		}
		if err := context.claim("request/"+string(value.Request.ID)+"/definition", value.Request, ref); err != nil {
			return domain.FactApplication{}, err
		}
		context.snapshot.Requests = append(context.snapshot.Requests, value.Request)
		context.snapshot.Units = append(context.snapshot.Units, value.Units...)
		context.snapshot.Cargo = append(context.snapshot.Cargo, value.Cargo...)
		context.snapshot.SourceRefs = append(context.snapshot.SourceRefs, value.SourceRef)
		add("request", string(value.Request.ID), "/requests", "/units", "/cargo")
	case domain.RequestCanceledFact:
		if err := context.claim("request/"+string(value.RequestID)+"/terminal_state", "canceled", ref); err != nil {
			return domain.FactApplication{}, err
		}
		if _, alreadyCanceled := context.canceled[value.RequestID]; !alreadyCanceled {
			if objects := context.protected.objectsForRequest(*context.snapshot, value.RequestID); len(objects) > 0 {
				return domain.FactApplication{}, &ManualReviewError{Code: "cancel_protected_execution", Objects: objects}
			}
			if !removeRequest(context.snapshot, value.RequestID) {
				return domain.FactApplication{}, fmt.Errorf("cancel fact references unknown request %q", value.RequestID)
			}
			context.canceled[value.RequestID] = struct{}{}
		}
		add("request", string(value.RequestID), "/requests", "/units", "/cargo")
	case domain.TaskCompletedFact:
		if value.CompletedAt.IsZero() || value.CompletedAt.After(ledgerFact.Fact.FactHeader().ObservedAt) {
			return domain.FactApplication{}, fmt.Errorf("completion fact for task %q has an invalid completed_at", value.TaskID)
		}
		if !problemHasTask(*context.snapshot, value.TaskID) {
			return domain.FactApplication{}, fmt.Errorf("completion fact references unknown task %q", value.TaskID)
		}
		if !activeAssignmentMatches(context.active, value.TaskID, value.VehicleID, value.DriverID) {
			return domain.FactApplication{}, &ManualReviewError{Code: "completion_assignment_conflict", Objects: []domain.ObjectRef{{Kind: "task", ID: string(value.TaskID)}}}
		}
		commitment := domain.ExecutedTaskCommitment{TaskID: value.TaskID, VehicleID: value.VehicleID, DriverID: value.DriverID, CompletedAt: value.CompletedAt.UTC()}
		if err := context.claim("task/"+string(value.TaskID)+"/completion", commitment, ref); err != nil {
			return domain.FactApplication{}, err
		}
		if err := appendExecuted(&context.snapshot.Commitments.Executed, commitment); err != nil {
			return domain.FactApplication{}, err
		}
		context.protected.tasks[value.TaskID] = struct{}{}
		context.protected.vehicles[value.VehicleID] = struct{}{}
		context.protected.drivers[value.DriverID] = struct{}{}
		add("task", string(value.TaskID), "/commitments/executed")
		if cargoIDs, unload := cargoUnloadedByTask(
			*context.snapshot,
			value.TaskID,
		); unload {
			for _, cargoID := range cargoIDs {
				context.unloaded[cargoID] = struct{}{}
				add("cargo", string(cargoID), "/commitments/in_transit")
			}
		}
	case domain.VehicleUnavailableFact:
		if value.UnavailableFrom.IsZero() {
			return domain.FactApplication{}, fmt.Errorf("vehicle unavailable_from is required")
		}
		if _, protected := context.protected.vehicles[value.VehicleID]; protected && !value.UnavailableFrom.After(context.createdAt) {
			return domain.FactApplication{}, &ManualReviewError{Code: "vehicle_unavailable_during_protected_execution", Objects: []domain.ObjectRef{{Kind: "vehicle", ID: string(value.VehicleID)}}}
		}
		if err := context.claim("vehicle/"+string(value.VehicleID)+"/unavailable_from", value.UnavailableFrom.UTC(), ref); err != nil {
			return domain.FactApplication{}, err
		}
		if !truncateVehicleAvailability(context.snapshot, value.VehicleID, value.UnavailableFrom.UTC()) {
			return domain.FactApplication{}, fmt.Errorf("vehicle fact references unknown vehicle %q", value.VehicleID)
		}
		add("vehicle", string(value.VehicleID), "/vehicles/availability")
	case domain.DriverUnavailableFact:
		if value.UnavailableFrom.IsZero() {
			return domain.FactApplication{}, fmt.Errorf("driver unavailable_from is required")
		}
		if _, protected := context.protected.drivers[value.DriverID]; protected && !value.UnavailableFrom.After(context.createdAt) {
			return domain.FactApplication{}, &ManualReviewError{Code: "driver_unavailable_during_protected_execution", Objects: []domain.ObjectRef{{Kind: "driver", ID: string(value.DriverID)}}}
		}
		if err := context.claim("driver/"+string(value.DriverID)+"/unavailable_from", value.UnavailableFrom.UTC(), ref); err != nil {
			return domain.FactApplication{}, err
		}
		if !truncateDriverShift(context.snapshot, value.DriverID, value.UnavailableFrom.UTC()) {
			return domain.FactApplication{}, fmt.Errorf("driver fact references unknown driver %q", value.DriverID)
		}
		add("driver", string(value.DriverID), "/drivers/shift/end")
	case domain.ChargerUnavailableFact:
		if value.UnavailableFrom.IsZero() {
			return domain.FactApplication{}, fmt.Errorf("charger unavailable_from is required")
		}
		if _, protected := context.protected.chargers[value.ChargerID]; protected && !value.UnavailableFrom.After(context.createdAt) {
			return domain.FactApplication{}, &ManualReviewError{Code: "charger_unavailable_during_protected_execution", Objects: []domain.ObjectRef{{Kind: "charger", ID: string(value.ChargerID)}}}
		}
		if err := context.claim("charger/"+string(value.ChargerID)+"/unavailable_from", value.UnavailableFrom.UTC(), ref); err != nil {
			return domain.FactApplication{}, err
		}
		if !truncateChargerAvailability(context.snapshot, value.ChargerID, value.UnavailableFrom.UTC()) {
			return domain.FactApplication{}, fmt.Errorf("charger fact references unknown charger %q", value.ChargerID)
		}
		add("charger", string(value.ChargerID), "/chargers/availability")
	case domain.TravelMatrixChangedFact:
		if err := context.claim("travel_matrix/current", struct {
			Travel domain.TravelMatrix `json:"travel"`
			Energy domain.EnergyMatrix `json:"energy"`
		}{Travel: value.Travel, Energy: value.Energy}, ref); err != nil {
			return domain.FactApplication{}, err
		}
		context.snapshot.Travel = value.Travel
		context.snapshot.Energy = value.Energy
		add("travel_matrix", "current", "/travel", "/energy")
	case domain.VehicleSOCObservedFact:
		if err := context.claim("vehicle/"+string(value.VehicleID)+"/initial_soc_wh", value.SOCWh, ref); err != nil {
			return domain.FactApplication{}, err
		}
		if err := updateVehicleSOC(context.snapshot, value.VehicleID, value.SOCWh); err != nil {
			return domain.FactApplication{}, err
		}
		add("vehicle", string(value.VehicleID), "/vehicles/energy/initial_soc_wh")
	case domain.ETADeviationFact:
		if !problemHasTask(*context.snapshot, value.TaskID) || value.ProjectedServiceAt.IsZero() {
			return domain.FactApplication{}, fmt.Errorf("ETA fact references an unknown task or zero projection")
		}
		if err := context.claim("task/"+string(value.TaskID)+"/projected_service_at", value.ProjectedServiceAt.UTC(), ref); err != nil {
			return domain.FactApplication{}, err
		}
		context.projectedETA[value.TaskID] = value.ProjectedServiceAt.UTC()
		add("task", string(value.TaskID), "/commitments/soft/planned_service_at")
	case domain.GuardianAssignmentFact:
		if !problemHasTask(*context.snapshot, value.TaskID) || !problemHasVehicle(*context.snapshot, value.VehicleID) || !problemHasDriver(*context.snapshot, value.DriverID) || value.PromisedServiceAt.IsZero() || value.ToleranceSeconds < 0 {
			return domain.FactApplication{}, fmt.Errorf("guardian assignment fact is incomplete")
		}
		commitment := domain.FrozenTaskCommitment{TaskID: value.TaskID, VehicleID: value.VehicleID, DriverID: value.DriverID, Sequence: value.Sequence, PromisedServiceAt: value.PromisedServiceAt.UTC(), ToleranceSeconds: value.ToleranceSeconds}
		if err := context.claim("task/"+string(value.TaskID)+"/guardian_assignment", commitment, ref); err != nil {
			return domain.FactApplication{}, err
		}
		context.guardian[value.TaskID] = commitment
		add("task", string(value.TaskID), "/commitments/frozen")
	default:
		return domain.FactApplication{}, fmt.Errorf("unsupported operational fact type %T", ledgerFact.Fact)
	}
	normalizeFactApplication(&application)
	return application, nil
}

func (context *reducerContext) claim(key string, value any, ref domain.FactRef) error {
	digest, err := domain.Digest(value)
	if err != nil {
		return fmt.Errorf("digest semantic fact claim %q: %w", key, err)
	}
	existing, exists := context.claims[key]
	if exists && existing.stream != ref.Position.Stream && existing.digest != digest {
		return &SemanticFactConflictError{Key: key, Existing: existing.fact, Incoming: ref}
	}
	context.claims[key] = terminalClaim{stream: ref.Position.Stream, digest: digest, fact: ref}
	return nil
}

func normalizeFactApplication(value *domain.FactApplication) {
	slices.SortFunc(value.Objects, func(left, right domain.ObjectRef) int {
		if result := strings.Compare(left.Kind, right.Kind); result != 0 {
			return result
		}
		return strings.Compare(left.ID, right.ID)
	})
	value.Objects = slices.Compact(value.Objects)
	slices.Sort(value.FieldPaths)
	value.FieldPaths = slices.Compact(value.FieldPaths)
}

func removeRequest(snapshot *domain.ProblemSnapshot, requestID domain.RequestID) bool {
	unitIDs := make(map[domain.FulfillmentUnitID]struct{})
	found := false
	requests := snapshot.Requests[:0]
	for _, request := range snapshot.Requests {
		if request.ID != requestID {
			requests = append(requests, request)
			continue
		}
		found = true
		for _, unitID := range request.UnitIDs {
			unitIDs[unitID] = struct{}{}
		}
	}
	if !found {
		return false
	}
	snapshot.Requests = requests
	units := snapshot.Units[:0]
	cargoIDs := make(map[domain.CargoID]struct{})
	for _, unit := range snapshot.Units {
		if _, remove := unitIDs[unit.ID]; remove {
			for _, cargoID := range unit.CargoIDs {
				cargoIDs[cargoID] = struct{}{}
			}
			continue
		}
		units = append(units, unit)
	}
	snapshot.Units = units
	cargo := snapshot.Cargo[:0]
	for _, item := range snapshot.Cargo {
		if _, remove := cargoIDs[item.ID]; !remove {
			cargo = append(cargo, item)
		}
	}
	snapshot.Cargo = cargo
	return true
}

func truncateVehicleAvailability(
	snapshot *domain.ProblemSnapshot,
	vehicleID domain.VehicleID,
	at time.Time,
) bool {
	for index := range snapshot.Vehicles {
		if snapshot.Vehicles[index].ID == vehicleID {
			snapshot.Vehicles[index].Availability = truncateRanges(
				snapshot.Vehicles[index].Availability,
				at,
			)
			return true
		}
	}
	return false
}

func truncateDriverShift(
	snapshot *domain.ProblemSnapshot,
	driverID domain.DriverID,
	at time.Time,
) bool {
	for index := range snapshot.Drivers {
		driver := &snapshot.Drivers[index]
		if driver.ID != driverID {
			continue
		}
		if at.After(driver.Shift.Start) && at.Before(driver.Shift.End) {
			driver.Shift.End = at
		} else if !at.After(driver.Shift.Start) {
			driver.Shift.End = driver.Shift.Start.Add(time.Nanosecond)
		}
		return true
	}
	return false
}

func truncateChargerAvailability(
	snapshot *domain.ProblemSnapshot,
	chargerID domain.ChargerID,
	at time.Time,
) bool {
	for index := range snapshot.Chargers {
		if snapshot.Chargers[index].ID == chargerID {
			snapshot.Chargers[index].Availability = truncateRanges(
				snapshot.Chargers[index].Availability,
				at,
			)
			return true
		}
	}
	return false
}

func truncateRanges(values []domain.TimeRange, at time.Time) []domain.TimeRange {
	result := make([]domain.TimeRange, 0, len(values))
	for _, value := range values {
		switch {
		case !value.End.After(at):
			result = append(result, value)
		case value.Start.Before(at):
			value.End = at
			result = append(result, value)
		}
	}
	return result
}

func updateVehicleSOC(
	snapshot *domain.ProblemSnapshot,
	vehicleID domain.VehicleID,
	socWh int64,
) error {
	for index := range snapshot.Vehicles {
		vehicle := &snapshot.Vehicles[index]
		if vehicle.ID != vehicleID {
			continue
		}
		if vehicle.Energy.Kind != domain.EnergyElectric || socWh < 0 ||
			socWh > vehicle.Energy.BatteryCapacityWh {
			return fmt.Errorf("SOC fact is invalid for vehicle %q", vehicleID)
		}
		vehicle.Energy.InitialSOCWh = socWh
		return nil
	}
	return fmt.Errorf("SOC fact references unknown vehicle %q", vehicleID)
}

func appendExecuted(
	values *[]domain.ExecutedTaskCommitment,
	wanted domain.ExecutedTaskCommitment,
) error {
	for _, value := range *values {
		if value.TaskID != wanted.TaskID {
			continue
		}
		if value != wanted {
			return fmt.Errorf("executed commitment for task %q is immutable", wanted.TaskID)
		}
		return nil
	}
	*values = append(*values, wanted)
	return nil
}
