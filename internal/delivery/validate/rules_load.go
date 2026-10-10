package validate

import (
	"fmt"
	"slices"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func (state *validationState) validateGeometry() {
	for dutyIndex, duty := range state.plan.Duties {
		vehicle, exists := state.vehicles[duty.VehicleID]
		if !exists {
			continue
		}
		compartments := compartmentIndex(vehicle)
		for tripIndex, trip := range duty.Trips {
			for _, stage := range trip.LoadStages {
				seen := make(map[domain.CargoID]struct{}, len(stage.Placements))
				for placementIndex, placement := range stage.Placements {
					at := atStop(dutyIndex, tripIndex, int(stage.AfterStopIndex))
					cargo, cargoExists := state.cargo[placement.CargoID]
					compartment, compartmentExists := compartments[placement.CompartmentID]
					if _, duplicate := seen[placement.CargoID]; duplicate {
						state.add("V704", domain.SeverityError, "cargo", ref(placement.CargoID),
							"one placement per stage", "duplicate placement", at)
					}
					seen[placement.CargoID] = struct{}{}
					if !cargoExists || !compartmentExists {
						state.add("V701", domain.SeverityError, "cargo", ref(placement.CargoID),
							"known cargo and compartment", "unknown reference", at)
						continue
					}
					oriented, allowed := cargo.SizeMM.Oriented(placement.Orientation)
					if !allowed ||
						!slices.Contains(cargo.AllowedOrientations, placement.Orientation) ||
						oriented != placement.SizeMM {
						state.add("V702", domain.SeverityError, "cargo", ref(placement.CargoID),
							fmt.Sprintf("allowed oriented size %+v", oriented),
							fmt.Sprintf("%+v", placement.SizeMM), at)
					}
					box := placement.Cuboid()
					if !compartment.Bounds.Contains(box) {
						state.add("V701", domain.SeverityError, "cargo", ref(placement.CargoID),
							"placement is inside compartment bounds",
							fmt.Sprintf("placement %d is outside", placementIndex), at)
					}
					for _, obstacle := range compartment.Obstacles {
						if box.IntersectsOpen(obstacle) {
							state.add("V703", domain.SeverityError, "cargo", ref(placement.CargoID),
								"placement avoids compartment obstacles",
								"placement intersects an obstacle", at)
						}
					}
				}
				for left := 0; left < len(stage.Placements); left++ {
					for right := left + 1; right < len(stage.Placements); right++ {
						first := stage.Placements[left]
						second := stage.Placements[right]
						if first.CompartmentID == second.CompartmentID &&
							first.Cuboid().IntersectsOpen(second.Cuboid()) {
							state.add("V704", domain.SeverityError, "cargo", ref(first.CargoID),
								"placements do not overlap", string(second.CargoID), atStop(
									dutyIndex, tripIndex, int(stage.AfterStopIndex),
								),
								domain.ObjectRef{Kind: "cargo", ID: string(second.CargoID)})
						}
					}
				}
			}
		}
	}
}

func compartmentIndex(vehicle domain.Vehicle) map[domain.CompartmentID]domain.Compartment {
	result := make(map[domain.CompartmentID]domain.Compartment, len(vehicle.Compartments))
	for _, value := range vehicle.Compartments {
		result[value.ID] = value
	}
	return result
}

type rectangle struct {
	minX int64
	maxX int64
	minY int64
	maxY int64
}

func (state *validationState) validateSupport() {
	for dutyIndex, duty := range state.plan.Duties {
		vehicle, exists := state.vehicles[duty.VehicleID]
		if !exists {
			continue
		}
		compartments := compartmentIndex(vehicle)
		for tripIndex, trip := range duty.Trips {
			for _, stage := range trip.LoadStages {
				byCargo := make(map[domain.CargoID]domain.Placement, len(stage.Placements))
				for _, placement := range stage.Placements {
					byCargo[placement.CargoID] = placement
				}
				supports := make(map[domain.CargoID][]domain.CargoID)
				for _, placement := range stage.Placements {
					cargo, cargoExists := state.cargo[placement.CargoID]
					compartment, compartmentExists := compartments[placement.CompartmentID]
					if !cargoExists || !compartmentExists {
						continue
					}
					floorZ := compartment.Bounds.Origin.Z
					if placement.PositionMM.Z == floorZ {
						continue
					}
					areas := make([]rectangle, 0)
					for _, candidate := range stage.Placements {
						if candidate.CargoID == placement.CargoID ||
							candidate.CompartmentID != placement.CompartmentID ||
							candidate.PositionMM.Z+candidate.SizeMM.Height !=
								placement.PositionMM.Z {
							continue
						}
						intersection := intersectionRectangle(
							placement.Cuboid(), candidate.Cuboid(),
						)
						if intersection.maxX > intersection.minX &&
							intersection.maxY > intersection.minY {
							areas = append(areas, intersection)
							supports[candidate.CargoID] =
								append(supports[candidate.CargoID], placement.CargoID)
						}
					}
					supportedArea := rectangleUnionArea(areas)
					baseArea := placement.SizeMM.Length * placement.SizeMM.Width
					supportPPM := int64(0)
					if baseArea > 0 {
						supportPPM = supportedArea * 1_000_000 / baseArea
					}
					required := cargo.MinSupportPPM
					if required == 0 {
						required = state.problem.Policy.DefaultMinSupportPPM
					}
					if supportPPM < required {
						state.add("V801", domain.SeverityError, "cargo", ref(cargo.ID),
							formatInt(required), formatInt(supportPPM),
							atStop(dutyIndex, tripIndex, int(stage.AfterStopIndex)))
					}
				}
				for cargoID := range byCargo {
					cargo := state.cargo[cargoID]
					topLoad := cumulativeSupportedWeight(cargoID, supports, state.cargo, make(map[domain.CargoID]bool))
					if cargo.FragileTopOnly && topLoad > 0 {
						state.add("V802", domain.SeverityError, "cargo", ref(cargoID),
							"no cargo above fragile item", formatInt(topLoad),
							atStop(dutyIndex, tripIndex, int(stage.AfterStopIndex)))
					}
					if topLoad > cargo.MaxTopLoadG {
						state.add("V803", domain.SeverityError, "cargo", ref(cargoID),
							formatInt(cargo.MaxTopLoadG), formatInt(topLoad),
							atStop(dutyIndex, tripIndex, int(stage.AfterStopIndex)))
					}
				}
			}
		}
	}
}

func intersectionRectangle(left, right domain.Cuboid) rectangle {
	leftMax := left.Max()
	rightMax := right.Max()
	return rectangle{
		minX: max64(left.Origin.X, right.Origin.X),
		maxX: min64(leftMax.X, rightMax.X),
		minY: max64(left.Origin.Y, right.Origin.Y),
		maxY: min64(leftMax.Y, rightMax.Y),
	}
}

func rectangleUnionArea(values []rectangle) int64 {
	if len(values) == 0 {
		return 0
	}
	xs := make([]int64, 0, len(values)*2)
	for _, value := range values {
		xs = append(xs, value.minX, value.maxX)
	}
	slices.Sort(xs)
	xs = slices.Compact(xs)
	var area int64
	for index := 0; index+1 < len(xs); index++ {
		minX := xs[index]
		maxX := xs[index+1]
		if minX == maxX {
			continue
		}
		intervals := make([][2]int64, 0)
		for _, value := range values {
			if value.minX < maxX && value.maxX > minX {
				intervals = append(intervals, [2]int64{value.minY, value.maxY})
			}
		}
		slices.SortFunc(intervals, func(left, right [2]int64) int {
			if left[0] < right[0] {
				return -1
			}
			if left[0] > right[0] {
				return 1
			}
			return 0
		})
		var coveredY int64
		if len(intervals) > 0 {
			start, end := intervals[0][0], intervals[0][1]
			for _, interval := range intervals[1:] {
				if interval[0] > end {
					coveredY += end - start
					start, end = interval[0], interval[1]
					continue
				}
				if interval[1] > end {
					end = interval[1]
				}
			}
			coveredY += end - start
		}
		area += (maxX - minX) * coveredY
	}
	return area
}

func cumulativeSupportedWeight(
	cargoID domain.CargoID,
	supports map[domain.CargoID][]domain.CargoID,
	cargo map[domain.CargoID]domain.CargoItem,
	visited map[domain.CargoID]bool,
) int64 {
	var result int64
	for _, supportedID := range supports[cargoID] {
		if visited[supportedID] {
			continue
		}
		visited[supportedID] = true
		result += cargo[supportedID].WeightG
		result += cumulativeSupportedWeight(supportedID, supports, cargo, visited)
	}
	return result
}

func (state *validationState) validateExtraction() {
	for dutyIndex, duty := range state.plan.Duties {
		vehicle, exists := state.vehicles[duty.VehicleID]
		if !exists {
			continue
		}
		doors := make(map[domain.DoorID]domain.Door, len(vehicle.Doors))
		for _, door := range vehicle.Doors {
			doors[door.ID] = door
		}
		for tripIndex, trip := range duty.Trips {
			stageByStop := make(map[uint32]domain.LoadStage, len(trip.LoadStages))
			for _, stage := range trip.LoadStages {
				if _, duplicate := stageByStop[stage.AfterStopIndex]; duplicate {
					state.add("V903", domain.SeverityError, "trip", ref(trip.ID),
						"one load stage per stop index", "duplicate load stage index",
						atStop(dutyIndex, tripIndex, int(stage.AfterStopIndex)))
				}
				stageByStop[stage.AfterStopIndex] = stage
				if int(stage.AfterStopIndex) >= len(trip.Stops) {
					state.add("V903", domain.SeverityError, "trip", ref(trip.ID),
						"load stage index refers to a trip stop",
						fmt.Sprintf("%d", stage.AfterStopIndex),
						atStop(dutyIndex, tripIndex, int(stage.AfterStopIndex)))
				}
				if len(stage.Rehandles) > int(state.problem.Policy.MaxRehandlesPerStop) {
					state.add("V903", domain.SeverityError, "trip", ref(trip.ID),
						fmt.Sprintf("at most %d rehandles", state.problem.Policy.MaxRehandlesPerStop),
						fmt.Sprintf("%d rehandles", len(stage.Rehandles)),
						atStop(dutyIndex, tripIndex, int(stage.AfterStopIndex)))
				}
				if stage.AfterStopIndex == 0 && len(stage.Rehandles) > 0 {
					state.add("V905", domain.SeverityError, "trip", ref(trip.ID),
						"the first load stage has no rehandle operations",
						fmt.Sprintf("%d rehandles", len(stage.Rehandles)),
						atStop(dutyIndex, tripIndex, int(stage.AfterStopIndex)))
				}
				for _, placement := range stage.Placements {
					state.validatePlacementTaskRefs(
						dutyIndex, tripIndex, stage, placement,
					)
					door, doorExists := doors[placement.DoorID]
					if !doorExists || door.CompartmentID != placement.CompartmentID {
						state.add("V901", domain.SeverityError, "cargo", ref(placement.CargoID),
							"placement references a compatible vehicle door",
							string(placement.DoorID),
							atStop(dutyIndex, tripIndex, int(stage.AfterStopIndex)))
					}
				}
			}

			expected := make(map[domain.CargoID]struct{})
			var previous domain.LoadStage
			for stopIndex, stop := range trip.Stops {
				removed := make(map[domain.CargoID]struct{})
				for _, taskID := range stop.TaskIDs {
					definition, taskExists := state.tasks[taskID]
					if !taskExists {
						continue
					}
					for _, unitID := range definition.task.UnitIDs {
						unit, unitExists := state.units[unitID]
						if !unitExists {
							continue
						}
						for _, cargoID := range unit.CargoIDs {
							switch definition.task.Kind {
							case domain.TaskPickup, domain.TaskDepotLoad:
								expected[cargoID] = struct{}{}
							case domain.TaskDelivery, domain.TaskDepotUnload:
								delete(expected, cargoID)
								removed[cargoID] = struct{}{}
							}
						}
					}
				}
				stage, stageExists := stageByStop[uint32(stopIndex)]
				if !stageExists {
					state.add("V903", domain.SeverityError, "trip", ref(trip.ID),
						"one load stage after every stop", "load stage is missing",
						atStop(dutyIndex, tripIndex, stopIndex))
					continue
				}
				actual := make(map[domain.CargoID]struct{}, len(stage.Placements))
				for _, placement := range stage.Placements {
					actual[placement.CargoID] = struct{}{}
				}
				if !equalCargoSets(expected, actual) {
					state.add("V903", domain.SeverityError, "trip", ref(trip.ID),
						"load stage matches pickup and delivery effects",
						"load stage cargo set differs",
						atStop(dutyIndex, tripIndex, stopIndex))
				}
				if stopIndex > 0 {
					state.validateStageTransition(
						dutyIndex, tripIndex, stopIndex, doors, previous, stage, removed,
					)
				}
				previous = stage
			}
		}
	}
}

func (state *validationState) validateStageTransition(
	dutyIndex int,
	tripIndex int,
	stopIndex int,
	doors map[domain.DoorID]domain.Door,
	previous domain.LoadStage,
	current domain.LoadStage,
	removed map[domain.CargoID]struct{},
) {
	before := make(map[domain.CargoID]domain.Placement, len(previous.Placements))
	for _, placement := range previous.Placements {
		before[placement.CargoID] = placement
	}
	after := make(map[domain.CargoID]domain.Placement, len(current.Placements))
	for _, placement := range current.Placements {
		after[placement.CargoID] = placement
	}
	operations := make(map[domain.CargoID]domain.RehandleOperation, len(current.Rehandles))
	for operationIndex, operation := range current.Rehandles {
		if operation.Sequence != uint16(operationIndex+1) {
			state.add("V905", domain.SeverityError, "cargo", ref(operation.CargoID),
				fmt.Sprintf("rehandle sequence %d", operationIndex+1),
				fmt.Sprintf("%d", operation.Sequence),
				atStop(dutyIndex, tripIndex, stopIndex))
		}
		if _, duplicate := operations[operation.CargoID]; duplicate {
			state.add("V905", domain.SeverityError, "cargo", ref(operation.CargoID),
				"one rehandle operation per cargo and stop",
				"duplicate rehandle operation",
				atStop(dutyIndex, tripIndex, stopIndex))
		}
		operations[operation.CargoID] = operation
		prior, existed := before[operation.CargoID]
		next, remains := after[operation.CargoID]
		if !existed || !remains {
			state.add("V905", domain.SeverityError, "cargo", ref(operation.CargoID),
				"rehandled cargo survives the stop",
				"cargo does not exist on both sides of the stop",
				atStop(dutyIndex, tripIndex, stopIndex))
		}
		if operation.StopIndex != uint32(stopIndex) ||
			operation.Before.CargoID != operation.CargoID ||
			operation.After.CargoID != operation.CargoID ||
			operation.Before != prior ||
			operation.After != next {
			state.add("V905", domain.SeverityError, "cargo", ref(operation.CargoID),
				"operation stop and before/after placements match adjacent stages",
				"operation does not match the load-stage transition",
				atStop(dutyIndex, tripIndex, stopIndex))
		}
		if operation.DurationSeconds != state.problem.Policy.RehandleSecondsPerCargo ||
			operation.CostCents != state.problem.Policy.RehandleCostCentsPerCargo {
			state.add("V905", domain.SeverityError, "cargo", ref(operation.CargoID),
				fmt.Sprintf(
					"%d seconds and %d cents",
					state.problem.Policy.RehandleSecondsPerCargo,
					state.problem.Policy.RehandleCostCentsPerCargo,
				),
				fmt.Sprintf(
					"%d seconds and %d cents",
					operation.DurationSeconds,
					operation.CostCents,
				),
				atStop(dutyIndex, tripIndex, stopIndex))
		}
	}
	changed := make(map[domain.CargoID]struct{})
	blocking := make(map[domain.CargoID]struct{})
	for cargoID, placement := range after {
		prior, existed := before[cargoID]
		if !existed || prior == placement {
			continue
		}
		changed[cargoID] = struct{}{}
	}
	for _, placement := range previous.Placements {
		if _, shouldRemove := removed[placement.CargoID]; !shouldRemove {
			continue
		}
		door, exists := doors[placement.DoorID]
		if !exists {
			continue
		}
		corridor, corridorExists := extractionCorridor(placement.Cuboid(), door)
		if !corridorExists {
			state.add("V902", domain.SeverityError, "cargo", ref(placement.CargoID),
				"cargo aligns with extraction door", "door is not reachable",
				atStop(dutyIndex, tripIndex, stopIndex))
			continue
		}
		for cargoID, blocker := range after {
			if blocker.CompartmentID == placement.CompartmentID &&
				corridor.IntersectsOpen(blocker.Cuboid()) {
				blocking[cargoID] = struct{}{}
			}
		}
	}
	for cargoID := range changed {
		if _, declared := operations[cargoID]; !declared {
			state.add("V905", domain.SeverityError, "cargo", ref(cargoID),
				"an explicit rehandle operation records every changed placement",
				"placement changed without a rehandle operation",
				atStop(dutyIndex, tripIndex, stopIndex))
		}
	}
	for cargoID := range blocking {
		if _, declared := operations[cargoID]; !declared {
			state.add("V902", domain.SeverityError, "cargo", ref(cargoID),
				"an explicit rehandle operation clears every extraction blocker",
				"blocking cargo has no rehandle operation",
				atStop(dutyIndex, tripIndex, stopIndex))
		}
	}
	for cargoID := range operations {
		_, placementChanged := changed[cargoID]
		_, blocksExtraction := blocking[cargoID]
		if !placementChanged && !blocksExtraction {
			state.add("V905", domain.SeverityError, "cargo", ref(cargoID),
				"rehandle is required by a placement change or extraction blocker",
				"unnecessary rehandle operation",
				atStop(dutyIndex, tripIndex, stopIndex))
		}
	}
}

func (state *validationState) validatePlacementTaskRefs(
	dutyIndex int,
	tripIndex int,
	stage domain.LoadStage,
	placement domain.Placement,
) {
	cargo, cargoExists := state.cargo[placement.CargoID]
	load, loadExists := state.tasks[placement.LoadAtTaskID]
	unload, unloadExists := state.tasks[placement.UnloadAtTaskID]
	valid := cargoExists && loadExists && unloadExists
	if valid {
		valid = (load.task.Kind == domain.TaskPickup ||
			load.task.Kind == domain.TaskDepotLoad) &&
			(unload.task.Kind == domain.TaskDelivery ||
				unload.task.Kind == domain.TaskDepotUnload) &&
			slices.Contains(load.task.UnitIDs, cargo.UnitID) &&
			slices.Contains(unload.task.UnitIDs, cargo.UnitID)
	}
	loadVisits := state.visits[placement.LoadAtTaskID]
	unloadVisits := state.visits[placement.UnloadAtTaskID]
	if valid {
		valid = len(loadVisits) == 1 && len(unloadVisits) == 1 &&
			loadVisits[0].dutyIndex == dutyIndex &&
			loadVisits[0].tripIndex == tripIndex &&
			unloadVisits[0].dutyIndex == dutyIndex &&
			unloadVisits[0].tripIndex == tripIndex &&
			loadVisits[0].stopIndex <= int(stage.AfterStopIndex) &&
			int(stage.AfterStopIndex) < unloadVisits[0].stopIndex
	}
	if !valid {
		state.add("V904", domain.SeverityError, "cargo", ref(placement.CargoID),
			"load and unload tasks exist, own the cargo, and bound this stage",
			string(placement.LoadAtTaskID)+" / "+string(placement.UnloadAtTaskID),
			atStop(dutyIndex, tripIndex, int(stage.AfterStopIndex)))
	}
}

func equalCargoSets(left, right map[domain.CargoID]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for value := range left {
		if _, exists := right[value]; !exists {
			return false
		}
	}
	return true
}

func extractionCorridor(cargo domain.Cuboid, door domain.Door) (domain.Cuboid, bool) {
	cargoMax := cargo.Max()
	doorMax := door.Opening.Max()
	switch {
	case door.ExtractionAxis == domain.AxisX && door.Direction > 0:
		if door.Opening.Origin.X < cargoMax.X ||
			cargo.Origin.Y < door.Opening.Origin.Y || cargoMax.Y > doorMax.Y ||
			cargo.Origin.Z < door.Opening.Origin.Z || cargoMax.Z > doorMax.Z {
			return domain.Cuboid{}, false
		}
		return domain.Cuboid{
			Origin: domain.Point3{X: cargoMax.X, Y: cargo.Origin.Y, Z: cargo.Origin.Z},
			Size: domain.Box{
				Length: door.Opening.Origin.X - cargoMax.X,
				Width:  cargo.Size.Width,
				Height: cargo.Size.Height,
			},
		}, true
	case door.ExtractionAxis == domain.AxisX && door.Direction < 0:
		if doorMax.X > cargo.Origin.X ||
			cargo.Origin.Y < door.Opening.Origin.Y || cargoMax.Y > doorMax.Y ||
			cargo.Origin.Z < door.Opening.Origin.Z || cargoMax.Z > doorMax.Z {
			return domain.Cuboid{}, false
		}
		return domain.Cuboid{
			Origin: domain.Point3{X: doorMax.X, Y: cargo.Origin.Y, Z: cargo.Origin.Z},
			Size: domain.Box{
				Length: cargo.Origin.X - doorMax.X,
				Width:  cargo.Size.Width,
				Height: cargo.Size.Height,
			},
		}, true
	case door.ExtractionAxis == domain.AxisY && door.Direction > 0:
		if door.Opening.Origin.Y < cargoMax.Y ||
			cargo.Origin.X < door.Opening.Origin.X || cargoMax.X > doorMax.X ||
			cargo.Origin.Z < door.Opening.Origin.Z || cargoMax.Z > doorMax.Z {
			return domain.Cuboid{}, false
		}
		return domain.Cuboid{
			Origin: domain.Point3{X: cargo.Origin.X, Y: cargoMax.Y, Z: cargo.Origin.Z},
			Size: domain.Box{
				Length: cargo.Size.Length,
				Width:  door.Opening.Origin.Y - cargoMax.Y,
				Height: cargo.Size.Height,
			},
		}, true
	case door.ExtractionAxis == domain.AxisY && door.Direction < 0:
		if doorMax.Y > cargo.Origin.Y ||
			cargo.Origin.X < door.Opening.Origin.X || cargoMax.X > doorMax.X ||
			cargo.Origin.Z < door.Opening.Origin.Z || cargoMax.Z > doorMax.Z {
			return domain.Cuboid{}, false
		}
		return domain.Cuboid{
			Origin: domain.Point3{X: cargo.Origin.X, Y: doorMax.Y, Z: cargo.Origin.Z},
			Size: domain.Box{
				Length: cargo.Size.Length,
				Width:  cargo.Origin.Y - doorMax.Y,
				Height: cargo.Size.Height,
			},
		}, true
	default:
		return domain.Cuboid{}, false
	}
}

func (state *validationState) validateVehicleLoads() {
	for dutyIndex, duty := range state.plan.Duties {
		vehicle, exists := state.vehicles[duty.VehicleID]
		if !exists {
			continue
		}
		compartments := compartmentIndex(vehicle)
		for tripIndex, trip := range duty.Trips {
			for _, stage := range trip.LoadStages {
				at := atStop(dutyIndex, tripIndex, int(stage.AfterStopIndex))
				weightByCompartment := make(map[domain.CompartmentID]int64)
				var totalWeight int64
				var weightedX, weightedY, weightedZ int64
				for _, placement := range stage.Placements {
					cargo, cargoExists := state.cargo[placement.CargoID]
					if !cargoExists {
						continue
					}
					weightByCompartment[placement.CompartmentID] += cargo.WeightG
					totalWeight += cargo.WeightG
					center := domain.Point3{
						X: placement.PositionMM.X + placement.SizeMM.Length/2,
						Y: placement.PositionMM.Y + placement.SizeMM.Width/2,
						Z: placement.PositionMM.Z + placement.SizeMM.Height/2,
					}
					weightedX += center.X * cargo.WeightG
					weightedY += center.Y * cargo.WeightG
					weightedZ += center.Z * cargo.WeightG
				}
				for compartmentID, weight := range weightByCompartment {
					compartment, compartmentExists := compartments[compartmentID]
					if compartmentExists && weight > compartment.MaxPayloadG {
						state.add("V1001", domain.SeverityError, "compartment", ref(compartmentID),
							formatInt(compartment.MaxPayloadG), formatInt(weight), at)
					}
				}
				if vehicle.TareWeightG+totalWeight > vehicle.MaxGrossWeightG {
					state.add("V1001", domain.SeverityError, "vehicle", ref(vehicle.ID),
						formatInt(vehicle.MaxGrossWeightG),
						formatInt(vehicle.TareWeightG+totalWeight), at)
				}
				expectedCG := domain.Point3{}
				if totalWeight > 0 {
					expectedCG = domain.Point3{
						X: weightedX / totalWeight,
						Y: weightedY / totalWeight,
						Z: weightedZ / totalWeight,
					}
				}
				if stage.CenterOfMassMM != expectedCG ||
					!pointInEnvelope(expectedCG, vehicle.CGEnvelope) {
					state.add("V1002", domain.SeverityError, "vehicle", ref(vehicle.ID),
						fmt.Sprintf("center of mass %+v inside envelope", expectedCG),
						fmt.Sprintf("%+v", stage.CenterOfMassMM), at)
				}
				expectedAxles := axleLoads(vehicle.Axles, totalWeight, expectedCG.X)
				if !slices.Equal(stage.AxleLoadsG, expectedAxles) {
					state.add("V1003", domain.SeverityError, "vehicle", ref(vehicle.ID),
						fmt.Sprintf("%v", expectedAxles), fmt.Sprintf("%v", stage.AxleLoadsG), at)
				}
				for axleIndex, load := range expectedAxles {
					if axleIndex < len(vehicle.Axles) &&
						load > vehicle.Axles[axleIndex].MaxLoadG {
						state.add("V1003", domain.SeverityError, "axle",
							ref(vehicle.Axles[axleIndex].ID),
							formatInt(vehicle.Axles[axleIndex].MaxLoadG),
							formatInt(load), at)
					}
				}
			}
		}
	}
}

func pointInEnvelope(value domain.Point3, envelope domain.CGEnvelope) bool {
	return value.X >= envelope.Min.X && value.X <= envelope.Max.X &&
		value.Y >= envelope.Min.Y && value.Y <= envelope.Max.Y &&
		value.Z >= envelope.Min.Z && value.Z <= envelope.Max.Z
}

func axleLoads(axles []domain.Axle, totalWeight, centerX int64) []int64 {
	result := make([]int64, len(axles))
	if totalWeight == 0 || len(axles) == 0 {
		return result
	}
	type indexedAxle struct {
		index int
		axle  domain.Axle
	}
	ordered := make([]indexedAxle, len(axles))
	for index, axle := range axles {
		ordered[index] = indexedAxle{index: index, axle: axle}
	}
	slices.SortFunc(ordered, func(left, right indexedAxle) int {
		if left.axle.PositionXMM < right.axle.PositionXMM {
			return -1
		}
		if left.axle.PositionXMM > right.axle.PositionXMM {
			return 1
		}
		return 0
	})
	if centerX <= ordered[0].axle.PositionXMM {
		result[ordered[0].index] = totalWeight
		return result
	}
	last := ordered[len(ordered)-1]
	if centerX >= last.axle.PositionXMM {
		result[last.index] = totalWeight
		return result
	}
	for index := 0; index+1 < len(ordered); index++ {
		left := ordered[index]
		right := ordered[index+1]
		if centerX < left.axle.PositionXMM || centerX > right.axle.PositionXMM {
			continue
		}
		distance := right.axle.PositionXMM - left.axle.PositionXMM
		rightLoad := totalWeight * (centerX - left.axle.PositionXMM) / distance
		result[left.index] = totalWeight - rightLoad
		result[right.index] = rightLoad
		break
	}
	return result
}

func min64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func max64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
