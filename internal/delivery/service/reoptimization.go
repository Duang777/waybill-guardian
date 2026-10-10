package service

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type ReoptimizationDiff struct {
	BaseVersion       uint64                `json:"base_version"`
	SuccessorVersion  uint64                `json:"successor_version"`
	BaseProblemDigest domain.ArtifactDigest `json:"base_problem_digest"`
	NewProblemDigest  domain.ArtifactDigest `json:"new_problem_digest"`
	FactIDs           []string              `json:"fact_ids"`
	AddedRequests     []domain.RequestID    `json:"added_requests"`
	CanceledRequests  []domain.RequestID    `json:"canceled_requests"`
	CompletedTasks    []domain.TaskID       `json:"completed_tasks"`
	ChangedResources  []domain.ObjectRef    `json:"changed_resources"`
}

func BuildSuccessorSnapshot(
	base domain.ProblemSnapshot,
	active domain.Plan,
	facts []domain.OperationalFact,
	createdAt time.Time,
) (domain.ProblemSnapshot, ReoptimizationDiff, error) {
	if err := validateActivePlanBinding(base, active); err != nil {
		return domain.ProblemSnapshot{}, ReoptimizationDiff{}, err
	}
	if createdAt.IsZero() {
		return domain.ProblemSnapshot{}, ReoptimizationDiff{}, fmt.Errorf("created_at is required")
	}
	createdAt = createdAt.UTC()
	orderedFacts := append([]domain.OperationalFact(nil), facts...)
	slices.SortFunc(orderedFacts, func(left, right domain.OperationalFact) int {
		leftHeader := left.FactHeader()
		rightHeader := right.FactHeader()
		if order := leftHeader.OccurredAt.Compare(rightHeader.OccurredAt); order != 0 {
			return order
		}
		return strings.Compare(leftHeader.FactID, rightHeader.FactID)
	})
	if err := validateOperationalFacts(orderedFacts, createdAt); err != nil {
		return domain.ProblemSnapshot{}, ReoptimizationDiff{}, err
	}

	successor, err := cloneProblem(base)
	if err != nil {
		return domain.ProblemSnapshot{}, ReoptimizationDiff{}, err
	}
	successor.Version = base.Version + 1
	successor.CreatedAt = createdAt
	successor.ProblemDigest = ""
	successor.PolicyDigest = ""
	successor.CommitmentDigest = ""
	diff := ReoptimizationDiff{
		BaseVersion:       base.Version,
		SuccessorVersion:  successor.Version,
		BaseProblemDigest: base.ProblemDigest,
		FactIDs:           make([]string, 0, len(orderedFacts)),
		AddedRequests:     []domain.RequestID{},
		CanceledRequests:  []domain.RequestID{},
		CompletedTasks:    []domain.TaskID{},
		ChangedResources:  []domain.ObjectRef{},
	}
	guardianCommitments := make(map[domain.TaskID]domain.FrozenTaskCommitment)
	for _, fact := range orderedFacts {
		header := fact.FactHeader()
		diff.FactIDs = append(diff.FactIDs, header.FactID)
		successor.SourceRefs = append(successor.SourceRefs, domain.SourceRef{
			System:       header.SourceSystem,
			ResourceType: "operational_fact",
			ResourceID:   header.FactID,
			Version:      header.Watermark,
			ObservedAt:   header.OccurredAt,
		})
		if err := applyOperationalFact(
			&successor,
			fact,
			guardianCommitments,
			&diff,
		); err != nil {
			return domain.ProblemSnapshot{}, ReoptimizationDiff{}, err
		}
		successor.Commitments.FactWatermark = header.Watermark
	}
	successor.Commitments = deriveCommitments(
		successor,
		active,
		createdAt,
		guardianCommitments,
	)
	rebuilt, err := BuildProblemSnapshot(successor)
	if err != nil {
		return domain.ProblemSnapshot{}, ReoptimizationDiff{}, fmt.Errorf(
			"build successor problem: %w",
			err,
		)
	}
	diff.NewProblemDigest = rebuilt.ProblemDigest
	return rebuilt, diff, nil
}

func validateActivePlanBinding(
	base domain.ProblemSnapshot,
	active domain.Plan,
) error {
	digest, err := domain.ComputePlanDigest(active)
	if err != nil {
		return fmt.Errorf("digest active plan: %w", err)
	}
	if active.SchemaVersion != domain.PlanSchemaVersion ||
		active.ProblemDigest != base.ProblemDigest ||
		active.PolicyDigest != base.PolicyDigest ||
		active.CommitmentDigest != base.CommitmentDigest ||
		active.PlanDigest != digest {
		return fmt.Errorf("active plan is not bound to the base problem")
	}
	return nil
}

func validateOperationalFacts(
	facts []domain.OperationalFact,
	createdAt time.Time,
) error {
	seen := make(map[string]struct{}, len(facts))
	for index, fact := range facts {
		if fact == nil {
			return fmt.Errorf("operational fact %d is nil", index)
		}
		header := fact.FactHeader()
		if header.SchemaVersion != domain.OperationalFactSchemaVersion ||
			strings.TrimSpace(header.FactID) == "" ||
			header.OccurredAt.IsZero() ||
			header.OccurredAt.After(createdAt) ||
			strings.TrimSpace(header.Watermark) == "" ||
			strings.TrimSpace(header.SourceSystem) == "" {
			return fmt.Errorf("operational fact %d has an invalid header", index)
		}
		if _, duplicate := seen[header.FactID]; duplicate {
			return fmt.Errorf("duplicate operational fact %q", header.FactID)
		}
		seen[header.FactID] = struct{}{}
	}
	return nil
}

func applyOperationalFact(
	snapshot *domain.ProblemSnapshot,
	fact domain.OperationalFact,
	guardianCommitments map[domain.TaskID]domain.FrozenTaskCommitment,
	diff *ReoptimizationDiff,
) error {
	switch value := fact.(type) {
	case domain.NewRequestFact:
		snapshot.Requests = append(snapshot.Requests, value.Request)
		snapshot.Units = append(snapshot.Units, value.Units...)
		snapshot.Cargo = append(snapshot.Cargo, value.Cargo...)
		snapshot.SourceRefs = append(snapshot.SourceRefs, value.SourceRef)
		diff.AddedRequests = append(diff.AddedRequests, value.Request.ID)
	case domain.RequestCanceledFact:
		if !removeRequest(snapshot, value.RequestID) {
			return fmt.Errorf("cancel fact references unknown request %q", value.RequestID)
		}
		diff.CanceledRequests = append(diff.CanceledRequests, value.RequestID)
	case domain.TaskCompletedFact:
		if !problemHasTask(*snapshot, value.TaskID) {
			return fmt.Errorf("completion fact references unknown task %q", value.TaskID)
		}
		snapshot.Commitments.Executed = upsertExecuted(
			snapshot.Commitments.Executed,
			domain.ExecutedTaskCommitment{
				TaskID:      value.TaskID,
				VehicleID:   value.VehicleID,
				DriverID:    value.DriverID,
				CompletedAt: value.CompletedAt.UTC(),
			},
		)
		diff.CompletedTasks = append(diff.CompletedTasks, value.TaskID)
	case domain.VehicleUnavailableFact:
		found := false
		for index := range snapshot.Vehicles {
			if snapshot.Vehicles[index].ID == value.VehicleID {
				snapshot.Vehicles[index].Availability = truncateRanges(
					snapshot.Vehicles[index].Availability,
					value.UnavailableFrom.UTC(),
				)
				found = true
			}
		}
		if !found {
			return fmt.Errorf("vehicle fact references unknown vehicle %q", value.VehicleID)
		}
		diff.ChangedResources = append(diff.ChangedResources, domain.ObjectRef{
			Kind: "vehicle", ID: string(value.VehicleID),
		})
	case domain.DriverUnavailableFact:
		found := false
		for index := range snapshot.Drivers {
			driver := &snapshot.Drivers[index]
			if driver.ID != value.DriverID {
				continue
			}
			unavailableFrom := value.UnavailableFrom.UTC()
			if unavailableFrom.After(driver.Shift.Start) &&
				unavailableFrom.Before(driver.Shift.End) {
				driver.Shift.End = unavailableFrom
			} else if !unavailableFrom.After(driver.Shift.Start) {
				driver.Shift.End = driver.Shift.Start.Add(time.Nanosecond)
			}
			found = true
		}
		if !found {
			return fmt.Errorf("driver fact references unknown driver %q", value.DriverID)
		}
		diff.ChangedResources = append(diff.ChangedResources, domain.ObjectRef{
			Kind: "driver", ID: string(value.DriverID),
		})
	case domain.ChargerUnavailableFact:
		found := false
		for index := range snapshot.Chargers {
			if snapshot.Chargers[index].ID == value.ChargerID {
				snapshot.Chargers[index].Availability = truncateRanges(
					snapshot.Chargers[index].Availability,
					value.UnavailableFrom.UTC(),
				)
				found = true
			}
		}
		if !found {
			return fmt.Errorf("charger fact references unknown charger %q", value.ChargerID)
		}
		diff.ChangedResources = append(diff.ChangedResources, domain.ObjectRef{
			Kind: "charger", ID: string(value.ChargerID),
		})
	case domain.TravelMatrixChangedFact:
		snapshot.Travel = value.Travel
		snapshot.Energy = value.Energy
		diff.ChangedResources = append(diff.ChangedResources, domain.ObjectRef{
			Kind: "travel_matrix", ID: value.FactHeader().FactID,
		})
	case domain.VehicleSOCObservedFact:
		found := false
		for index := range snapshot.Vehicles {
			vehicle := &snapshot.Vehicles[index]
			if vehicle.ID != value.VehicleID {
				continue
			}
			if vehicle.Energy.Kind != domain.EnergyElectric ||
				value.SOCWh < 0 ||
				value.SOCWh > vehicle.Energy.BatteryCapacityWh {
				return fmt.Errorf("SOC fact is invalid for vehicle %q", value.VehicleID)
			}
			vehicle.Energy.InitialSOCWh = value.SOCWh
			found = true
		}
		if !found {
			return fmt.Errorf("SOC fact references unknown vehicle %q", value.VehicleID)
		}
		diff.ChangedResources = append(diff.ChangedResources, domain.ObjectRef{
			Kind: "vehicle_soc", ID: string(value.VehicleID),
		})
	case domain.ETADeviationFact:
		if !problemHasTask(*snapshot, value.TaskID) ||
			value.ProjectedServiceAt.IsZero() {
			return fmt.Errorf("ETA fact references an unknown task or zero projection")
		}
		diff.ChangedResources = append(diff.ChangedResources, domain.ObjectRef{
			Kind: "task_eta", ID: string(value.TaskID),
		})
	case domain.GuardianAssignmentFact:
		if !problemHasTask(*snapshot, value.TaskID) ||
			value.VehicleID == "" ||
			value.DriverID == "" ||
			value.PromisedServiceAt.IsZero() ||
			value.ToleranceSeconds < 0 {
			return fmt.Errorf("guardian assignment fact is incomplete")
		}
		guardianCommitments[value.TaskID] = domain.FrozenTaskCommitment{
			TaskID:            value.TaskID,
			VehicleID:         value.VehicleID,
			DriverID:          value.DriverID,
			Sequence:          value.Sequence,
			PromisedServiceAt: value.PromisedServiceAt.UTC(),
			ToleranceSeconds:  value.ToleranceSeconds,
		}
	default:
		return fmt.Errorf("unsupported operational fact type %T", fact)
	}
	return nil
}

func deriveCommitments(
	problem domain.ProblemSnapshot,
	active domain.Plan,
	at time.Time,
	guardian map[domain.TaskID]domain.FrozenTaskCommitment,
) domain.CommitmentSet {
	result := problem.Commitments
	result.BasePlanDigest = active.PlanDigest
	result.Frozen = []domain.FrozenTaskCommitment{}
	result.InTransit = []domain.InTransitCargoCommitment{}
	result.Soft = []domain.SoftTaskCommitment{}
	executed := make(map[domain.TaskID]struct{}, len(result.Executed))
	for _, commitment := range result.Executed {
		executed[commitment.TaskID] = struct{}{}
	}
	freezeUntil := at.Add(time.Duration(problem.Policy.FreezeWindowSeconds) * time.Second)
	for _, duty := range active.Duties {
		for _, trip := range duty.Trips {
			driverID := firstDriver(duty.DriverIDs)
			for stopIndex, stop := range trip.Stops {
				for _, taskID := range stop.TaskIDs {
					if !problemHasTask(problem, taskID) {
						continue
					}
					if _, done := executed[taskID]; done {
						continue
					}
					if commitment, exists := guardian[taskID]; exists {
						result.Frozen = append(result.Frozen, commitment)
						continue
					}
					if !stop.ServiceAt.After(freezeUntil) {
						result.Frozen = append(result.Frozen, domain.FrozenTaskCommitment{
							TaskID:            taskID,
							VehicleID:         duty.VehicleID,
							DriverID:          driverForTask(trip.Schedule, taskID, driverID),
							Sequence:          uint32(stopIndex),
							PromisedServiceAt: stop.ServiceAt,
							ToleranceSeconds:  problem.Policy.ETAToleranceSeconds,
						})
					} else {
						result.Soft = append(result.Soft, domain.SoftTaskCommitment{
							TaskID:           taskID,
							VehicleID:        duty.VehicleID,
							DriverID:         driverForTask(trip.Schedule, taskID, driverID),
							Sequence:         uint32(stopIndex),
							PlannedServiceAt: stop.ServiceAt,
						})
					}
				}
			}
			stage, exists := loadStageAt(trip, at)
			if !exists {
				continue
			}
			for _, placement := range stage.Placements {
				if !problemHasCargo(problem, placement.CargoID) {
					continue
				}
				result.InTransit = append(result.InTransit, domain.InTransitCargoCommitment{
					CargoID:       placement.CargoID,
					VehicleID:     duty.VehicleID,
					CompartmentID: placement.CompartmentID,
				})
			}
		}
	}
	return result
}

func removeRequest(snapshot *domain.ProblemSnapshot, requestID domain.RequestID) bool {
	unitIDs := make(map[domain.FulfillmentUnitID]struct{})
	taskIDs := make(map[domain.TaskID]struct{})
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
		for _, task := range request.Tasks {
			taskIDs[task.ID] = struct{}{}
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
	snapshot.Commitments.Executed = filterExecuted(snapshot.Commitments.Executed, taskIDs)
	snapshot.Commitments.Frozen = filterFrozen(snapshot.Commitments.Frozen, taskIDs)
	snapshot.Commitments.Soft = filterSoft(snapshot.Commitments.Soft, taskIDs)
	inTransit := snapshot.Commitments.InTransit[:0]
	for _, commitment := range snapshot.Commitments.InTransit {
		if _, remove := cargoIDs[commitment.CargoID]; !remove {
			inTransit = append(inTransit, commitment)
		}
	}
	snapshot.Commitments.InTransit = inTransit
	return true
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

func upsertExecuted(
	values []domain.ExecutedTaskCommitment,
	wanted domain.ExecutedTaskCommitment,
) []domain.ExecutedTaskCommitment {
	for index := range values {
		if values[index].TaskID == wanted.TaskID {
			values[index] = wanted
			return values
		}
	}
	return append(values, wanted)
}

func problemHasTask(problem domain.ProblemSnapshot, taskID domain.TaskID) bool {
	for _, request := range problem.Requests {
		for _, task := range request.Tasks {
			if task.ID == taskID {
				return true
			}
		}
	}
	return false
}

func problemHasCargo(problem domain.ProblemSnapshot, cargoID domain.CargoID) bool {
	for _, cargo := range problem.Cargo {
		if cargo.ID == cargoID {
			return true
		}
	}
	return false
}

func firstDriver(values []domain.DriverID) domain.DriverID {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func driverForTask(
	schedule []domain.DutySegment,
	taskID domain.TaskID,
	fallback domain.DriverID,
) domain.DriverID {
	for _, segment := range schedule {
		if slices.Contains(segment.TaskIDs, taskID) {
			return segment.DriverID
		}
	}
	return fallback
}

func loadStageAt(trip domain.Trip, at time.Time) (domain.LoadStage, bool) {
	var result domain.LoadStage
	found := false
	for _, stage := range trip.LoadStages {
		if int(stage.AfterStopIndex) >= len(trip.Stops) ||
			trip.Stops[stage.AfterStopIndex].DepartureAt.After(at) {
			continue
		}
		if !found || stage.AfterStopIndex > result.AfterStopIndex {
			result = stage
			found = true
		}
	}
	return result, found
}

func filterExecuted(
	values []domain.ExecutedTaskCommitment,
	remove map[domain.TaskID]struct{},
) []domain.ExecutedTaskCommitment {
	result := values[:0]
	for _, value := range values {
		if _, exists := remove[value.TaskID]; !exists {
			result = append(result, value)
		}
	}
	return result
}

func filterFrozen(
	values []domain.FrozenTaskCommitment,
	remove map[domain.TaskID]struct{},
) []domain.FrozenTaskCommitment {
	result := values[:0]
	for _, value := range values {
		if _, exists := remove[value.TaskID]; !exists {
			result = append(result, value)
		}
	}
	return result
}

func filterSoft(
	values []domain.SoftTaskCommitment,
	remove map[domain.TaskID]struct{},
) []domain.SoftTaskCommitment {
	result := values[:0]
	for _, value := range values {
		if _, exists := remove[value.TaskID]; !exists {
			result = append(result, value)
		}
	}
	return result
}
