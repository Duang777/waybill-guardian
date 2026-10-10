package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type planLeaf struct {
	domain     ComparisonDomain
	object     domain.ObjectRef
	fieldPath  string
	value      json.RawMessage
	executable bool
}

type taskVisitSnapshot struct {
	vehicleID   domain.VehicleID
	driverIDs   []domain.DriverID
	tripID      domain.TripID
	location    domain.LocationID
	index       uint32
	arrivalAt   time.Time
	serviceAt   time.Time
	departureAt time.Time
}

func flattenComparisonLeaves(
	problem domain.ProblemSnapshot,
	plan domain.Plan,
	effects ComparableEffectSet,
) (map[string]planLeaf, error) {
	result := make(map[string]planLeaf)
	add := func(comparisonDomain ComparisonDomain, object domain.ObjectRef, path string, value any, executable bool) error {
		raw, err := domain.CanonicalJSON(value)
		if err != nil {
			return fmt.Errorf("canonicalize comparison leaf %s: %w", path, err)
		}
		leaf := planLeaf{
			domain:     comparisonDomain,
			object:     object,
			fieldPath:  path,
			value:      raw,
			executable: executable,
		}
		key := comparisonLeafKey(leaf)
		if _, duplicate := result[key]; duplicate {
			return fmt.Errorf("duplicate comparison leaf %q", key)
		}
		result[key] = leaf
		return nil
	}
	visits := taskVisits(plan)
	for _, request := range problem.Requests {
		for _, unitID := range request.UnitIDs {
			assignment := unitAssignment(request, unitID, visits)
			if err := add(ComparisonRoute, domain.ObjectRef{Kind: "unit", ID: string(unitID)}, "/assignment", assignment, true); err != nil {
				return nil, err
			}
		}
	}
	for _, duty := range plan.Duties {
		vehicleObject := domain.ObjectRef{Kind: "vehicle", ID: string(duty.VehicleID)}
		if err := add(ComparisonRoute, vehicleObject, "/present", true, true); err != nil {
			return nil, err
		}
		if err := add(ComparisonSchedule, vehicleObject, "/driver_ids", duty.DriverIDs, true); err != nil {
			return nil, err
		}
		for _, trip := range duty.Trips {
			tripObject := domain.ObjectRef{Kind: "trip", ID: string(trip.ID)}
			routeValues := []struct {
				path  string
				value any
			}{{path: "/vehicle_id", value: duty.VehicleID}, {path: "/start_depot_id", value: trip.StartDepotID}, {path: "/end_depot_id", value: trip.EndDepotID}}
			for _, value := range routeValues {
				if err := add(ComparisonRoute, tripObject, value.path, value.value, true); err != nil {
					return nil, err
				}
			}
			if err := add(ComparisonSchedule, tripObject, "/start_at", trip.StartAt, true); err != nil {
				return nil, err
			}
			if err := add(ComparisonSchedule, tripObject, "/end_at", trip.EndAt, true); err != nil {
				return nil, err
			}
			for stopIndex, stop := range trip.Stops {
				stopObject := domain.ObjectRef{
					Kind: "stop",
					ID:   fmt.Sprintf("%s/%d", trip.ID, stopIndex),
				}
				if err := add(ComparisonRoute, stopObject, "/location_id", stop.LocationID, true); err != nil {
					return nil, err
				}
				if err := add(ComparisonRoute, stopObject, "/task_ids", stop.TaskIDs, true); err != nil {
					return nil, err
				}
				for _, taskID := range stop.TaskIDs {
					taskObject := domain.ObjectRef{Kind: "task", ID: string(taskID)}
					if err := add(ComparisonRoute, taskObject, "/stop_index", stopIndex, true); err != nil {
						return nil, err
					}
					scheduleValues := []struct {
						path  string
						value time.Time
					}{{path: "/arrival_at", value: stop.ArrivalAt}, {path: "/service_at", value: stop.ServiceAt}, {path: "/departure_at", value: stop.DepartureAt}}
					for _, value := range scheduleValues {
						if err := add(ComparisonSchedule, taskObject, value.path, value.value, true); err != nil {
							return nil, err
						}
					}
				}
				if stopIndex > 0 {
					legObject := domain.ObjectRef{
						Kind: "route_leg",
						ID:   fmt.Sprintf("%s/%d", trip.ID, stopIndex-1),
					}
					if err := add(ComparisonRoute, legObject, "/from_location_id", trip.Stops[stopIndex-1].LocationID, true); err != nil {
						return nil, err
					}
					if err := add(ComparisonRoute, legObject, "/to_location_id", stop.LocationID, true); err != nil {
						return nil, err
					}
				}
			}
			for segmentIndex, segment := range trip.Schedule {
				segmentObject := domain.ObjectRef{
					Kind: "duty_segment",
					ID:   fmt.Sprintf("%s/%d", trip.ID, segmentIndex),
				}
				if err := add(ComparisonSchedule, segmentObject, "/segment", segment, true); err != nil {
					return nil, err
				}
			}
			for legIndex, leg := range trip.Energy {
				if err := add(ComparisonEnergy, domain.ObjectRef{Kind: "energy_leg", ID: fmt.Sprintf("%s/%d", trip.ID, legIndex)}, "/energy", leg, true); err != nil {
					return nil, err
				}
			}
			for _, stage := range trip.LoadStages {
				stageID := fmt.Sprintf("%s/%d", trip.ID, stage.AfterStopIndex)
				stageObject := domain.ObjectRef{Kind: "load_stage", ID: stageID}
				if err := add(ComparisonLoad, stageObject, "/axle_loads_g", stage.AxleLoadsG, true); err != nil {
					return nil, err
				}
				if err := add(ComparisonLoad, stageObject, "/center_of_mass_mm", stage.CenterOfMassMM, true); err != nil {
					return nil, err
				}
				for _, placement := range stage.Placements {
					if err := add(ComparisonLoad, domain.ObjectRef{Kind: "cargo", ID: string(placement.CargoID)}, fmt.Sprintf("/load_stages/%d/placement", stage.AfterStopIndex), placement, true); err != nil {
						return nil, err
					}
				}
				for _, rehandle := range stage.Rehandles {
					if err := add(ComparisonLoad, domain.ObjectRef{Kind: "cargo", ID: string(rehandle.CargoID)}, fmt.Sprintf("/load_stages/%d/rehandles/%d", stage.AfterStopIndex, rehandle.Sequence), rehandle, true); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	for _, effect := range effects.Effects {
		object := domain.ObjectRef{Kind: "effect", ID: string(effect.ID)}
		values := []struct {
			path  string
			value any
		}{{path: "/action", value: effect.Action}, {path: "/target", value: effect.Target}, {path: "/required", value: effect.Required}, {path: "/parameters_digest", value: effect.ParametersDigest}}
		for _, value := range values {
			if err := add(ComparisonEffect, object, value.path, value.value, true); err != nil {
				return nil, err
			}
		}
	}
	for _, metric := range planMetricRegistry {
		if err := add(ComparisonMetric, domain.ObjectRef{Kind: "metric", ID: metric.name}, "/metrics/"+metric.name, metric.value(plan.Metrics), false); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func compareLeaves(
	base map[string]planLeaf,
	candidate map[string]planLeaf,
	input RevisionComparisonInput,
) ([]RevisionChange, error) {
	keys := make([]string, 0, len(base)+len(candidate))
	seen := make(map[string]struct{}, len(base)+len(candidate))
	for key := range base {
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for key := range candidate {
		if _, exists := seen[key]; !exists {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	nullValue := json.RawMessage("null")
	changes := make([]RevisionChange, 0)
	for _, key := range keys {
		before, beforeExists := base[key]
		after, afterExists := candidate[key]
		if beforeExists && afterExists && bytes.Equal(before.value, after.value) {
			continue
		}
		identity := before
		if !beforeExists {
			identity = after
		}
		beforeValue := nullValue
		if beforeExists {
			beforeValue = before.value
		}
		afterValue := nullValue
		if afterExists {
			afterValue = after.value
		}
		meta, err := buildChangeMeta(identity, beforeValue, afterValue, input)
		if err != nil {
			return nil, err
		}
		changes = append(
			changes,
			RevisionChange{
				Domain:     identity.domain,
				Object:     identity.object,
				FieldPath:  identity.fieldPath,
				Before:     beforeValue,
				After:      afterValue,
				Executable: identity.executable,
				Meta:       meta,
			},
		)
	}
	return changes, nil
}

func buildChangeMeta(
	leaf planLeaf,
	before json.RawMessage,
	after json.RawMessage,
	input RevisionComparisonInput,
) (ChangeMeta, error) {
	direct := make([]domain.FactRef, 0)
	for _, application := range input.Applications {
		if slices.Contains(application.Objects, leaf.object) {
			direct = append(direct, application.Fact)
		}
	}
	mode := AttributionDirect
	facts := direct
	if len(facts) == 0 {
		mode = AttributionDerived
		facts = append([]domain.FactRef(nil), input.AppliedFacts...)
	}
	overrideIDs := overrideIDsForObject(input.Override, leaf.object)
	if leaf.executable && len(facts) == 0 && len(overrideIDs) == 0 {
		return ChangeMeta{}, fmt.Errorf(
			"executable change %s %s lacks attribution",
			leaf.object.ID,
			leaf.fieldPath,
		)
	}
	changeDigest, err := domain.Digest(struct {
		Domain    ComparisonDomain `json:"domain"`
		Object    domain.ObjectRef `json:"object"`
		FieldPath string           `json:"field_path"`
		Before    json.RawMessage  `json:"before"`
		After     json.RawMessage  `json:"after"`
	}{Domain: leaf.domain, Object: leaf.object, FieldPath: leaf.fieldPath, Before: before, After: after})
	if err != nil {
		return ChangeMeta{}, err
	}
	policy := []PolicyRef{}
	if leaf.domain != ComparisonEffect {
		policy = append(
			policy,
			PolicyRef{
				PolicyID:    input.CandidateProblem.Policy.ID,
				Version:     input.CandidateProblem.Policy.Version,
				FieldPath:   "/",
				ValueDigest: input.CandidateProblem.PolicyDigest,
			},
		)
	}
	return ChangeMeta{
		ChangeID:    string(changeDigest),
		Facts:       facts,
		Policy:      policy,
		OverrideIDs: overrideIDs,
		Mode:        mode,
	}, nil
}

func overrideIDsForObject(
	override *VerifiedFreezeOverride,
	object domain.ObjectRef,
) []domain.ApprovalID {
	if override == nil {
		return []domain.ApprovalID{}
	}
	for _, scope := range override.grant.Scopes {
		switch object.Kind {
		case "task":
			if object.ID == string(scope.TaskID) {
				return []domain.ApprovalID{override.grant.ApprovalID}
			}
		case "cargo":
			if slices.Contains(scope.CargoIDs, domain.CargoID(object.ID)) {
				return []domain.ApprovalID{override.grant.ApprovalID}
			}
		}
	}
	return []domain.ApprovalID{}
}

func comparisonLeafKey(value planLeaf) string {
	return strings.Join(
		[]string{string(value.domain), value.object.Kind, value.object.ID, value.fieldPath},
		"\x00",
	)
}

func taskVisits(plan domain.Plan) map[domain.TaskID]taskVisitSnapshot {
	result := make(map[domain.TaskID]taskVisitSnapshot)
	for _, duty := range plan.Duties {
		for _, trip := range duty.Trips {
			for stopIndex, stop := range trip.Stops {
				for _, taskID := range stop.TaskIDs {
					result[taskID] = taskVisitSnapshot{
						vehicleID:   duty.VehicleID,
						driverIDs:   driversForTask(duty.DriverIDs, trip.Schedule, taskID),
						tripID:      trip.ID,
						location:    stop.LocationID,
						index:       uint32(stopIndex),
						arrivalAt:   stop.ArrivalAt,
						serviceAt:   stop.ServiceAt,
						departureAt: stop.DepartureAt,
					}
				}
			}
		}
	}
	return result
}

func driversForTask(
	dutyDrivers []domain.DriverID,
	schedule []domain.DutySegment,
	taskID domain.TaskID,
) []domain.DriverID {
	result := make([]domain.DriverID, 0)
	for _, segment := range schedule {
		if slices.Contains(segment.TaskIDs, taskID) && segment.DriverID != "" &&
			!slices.Contains(result, segment.DriverID) {
			result = append(result, segment.DriverID)
		}
	}
	if len(result) == 0 {
		result = append(result, dutyDrivers...)
	}
	slices.Sort(result)
	return result
}

func unitAssignment(
	request domain.TransportRequest,
	unitID domain.FulfillmentUnitID,
	visits map[domain.TaskID]taskVisitSnapshot,
) Agent3Assignment {
	var vehicleID domain.VehicleID
	found := false
	for _, task := range request.Tasks {
		if !slices.Contains(task.UnitIDs, unitID) {
			continue
		}
		visit, exists := visits[task.ID]
		if !exists {
			return Agent3Assignment{Kind: "unassigned"}
		}
		if found && visit.vehicleID != vehicleID {
			return Agent3Assignment{Kind: "unassigned"}
		}
		vehicleID = visit.vehicleID
		found = true
	}
	if !found {
		return Agent3Assignment{Kind: "unassigned"}
	}
	return Agent3Assignment{Kind: "assigned", VehicleID: vehicleID}
}
