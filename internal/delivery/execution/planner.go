package execution

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type Planner struct {
	Registry        *Registry
	RecoveryHorizon time.Duration
}

func (planner Planner) BuildEffectSet(
	tenantID domain.TenantID,
	revisionID domain.PlanRevisionID,
	plan domain.Plan,
) (domain.EffectSet, error) {
	if tenantID == "" || revisionID == "" ||
		plan.RevisionID != revisionID ||
		plan.PlanDigest == "" {
		return domain.EffectSet{}, fmt.Errorf("effect planner input is incomplete or mismatched")
	}
	effects := make([]domain.EffectPreview, 0)
	appendEffect := func(
		action domain.EffectAction,
		target string,
		parameters any,
		required bool,
	) error {
		raw, err := domain.CanonicalJSON(parameters)
		if err != nil {
			return err
		}
		parametersDigest, err := domain.DigestCanonicalJSON(raw)
		if err != nil {
			return err
		}
		identity, err := domain.Digest(struct {
			Version          string
			TenantID         domain.TenantID
			RevisionID       domain.PlanRevisionID
			Action           domain.EffectAction
			Target           string
			ParametersDigest domain.ArtifactDigest
		}{
			Version:          "delivery.effect-identity.v1",
			TenantID:         tenantID,
			RevisionID:       revisionID,
			Action:           action,
			Target:           target,
			ParametersDigest: parametersDigest,
		})
		if err != nil {
			return err
		}
		effects = append(effects, domain.EffectPreview{
			ID:               domain.EffectID(identity),
			Ordinal:          uint32(len(effects)),
			Action:           action,
			Target:           target,
			Parameters:       json.RawMessage(raw),
			ParametersDigest: parametersDigest,
			Required:         required,
		})
		return nil
	}
	for dutyIndex, duty := range plan.Duties {
		dutyTarget := string(plan.PlanID) + "/duty/" + strconv.Itoa(dutyIndex)
		if err := appendEffect(
			domain.EffectCreateRoute,
			dutyTarget,
			struct {
				PlanID     domain.PlanID         `json:"plan_id"`
				RevisionID domain.PlanRevisionID `json:"revision_id"`
				DutyIndex  int                   `json:"duty_index"`
				Trips      []domain.Trip         `json:"trips"`
			}{
				PlanID:     plan.PlanID,
				RevisionID: revisionID,
				DutyIndex:  dutyIndex,
				Trips:      duty.Trips,
			},
			true,
		); err != nil {
			return domain.EffectSet{}, err
		}
		if err := appendEffect(
			domain.EffectAssignVehicle,
			string(duty.VehicleID),
			map[string]any{
				"plan_id":     plan.PlanID,
				"revision_id": revisionID,
				"duty_index":  dutyIndex,
				"vehicle_id":  duty.VehicleID,
			},
			true,
		); err != nil {
			return domain.EffectSet{}, err
		}
		for driverIndex, driverID := range duty.DriverIDs {
			if err := appendEffect(
				domain.EffectAssignDriver,
				string(driverID),
				map[string]any{
					"plan_id":      plan.PlanID,
					"revision_id":  revisionID,
					"duty_index":   dutyIndex,
					"driver_index": driverIndex,
					"driver_id":    driverID,
				},
				true,
			); err != nil {
				return domain.EffectSet{}, err
			}
		}
		for tripIndex, trip := range duty.Trips {
			tripTarget := string(trip.ID)
			if err := appendEffect(
				domain.EffectPublishStops,
				tripTarget,
				map[string]any{
					"plan_id":     plan.PlanID,
					"revision_id": revisionID,
					"vehicle_id":  duty.VehicleID,
					"driver_ids":  duty.DriverIDs,
					"trip_index":  tripIndex,
					"trip_id":     trip.ID,
					"stops":       trip.Stops,
					"schedule":    trip.Schedule,
				},
				true,
			); err != nil {
				return domain.EffectSet{}, err
			}
			if err := appendEffect(
				domain.EffectPublishLoad,
				tripTarget,
				map[string]any{
					"plan_id":     plan.PlanID,
					"revision_id": revisionID,
					"vehicle_id":  duty.VehicleID,
					"trip_index":  tripIndex,
					"trip_id":     trip.ID,
					"load_stages": trip.LoadStages,
				},
				true,
			); err != nil {
				return domain.EffectSet{}, err
			}
			for stopIndex, stop := range trip.Stops {
				if len(stop.TaskIDs) == 0 {
					continue
				}
				if err := appendEffect(
					domain.EffectNotifyETA,
					tripTarget+"/stop/"+strconv.Itoa(stopIndex),
					map[string]any{
						"plan_id":     plan.PlanID,
						"revision_id": revisionID,
						"trip_id":     trip.ID,
						"stop_index":  stopIndex,
						"location_id": stop.LocationID,
						"task_ids":    stop.TaskIDs,
						"service_at":  stop.ServiceAt,
					},
					false,
				); err != nil {
					return domain.EffectSet{}, err
				}
			}
		}
	}
	effects, err := planner.Registry.BindPreviews(effects, planner.RecoveryHorizon)
	if err != nil {
		return domain.EffectSet{}, err
	}
	result := domain.EffectSet{
		SchemaVersion: domain.EffectSetSchemaVersion,
		TenantID:      tenantID,
		RevisionID:    revisionID,
		Effects:       effects,
	}
	digest, err := domain.Digest(result)
	if err != nil {
		return domain.EffectSet{}, err
	}
	result.Digest = digest
	return result, nil
}

func VerifyEffectSet(value domain.EffectSet) error {
	if value.SchemaVersion != domain.EffectSetSchemaVersion ||
		value.TenantID == "" ||
		value.RevisionID == "" ||
		value.Digest == "" {
		return fmt.Errorf("effect set is incomplete")
	}
	expected := value.Digest
	value.Digest = ""
	actual, err := domain.Digest(value)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("effect set digest mismatch")
	}
	seen := make(map[domain.EffectID]struct{}, len(value.Effects))
	for index, effect := range value.Effects {
		if effect.ID == "" ||
			effect.Ordinal != uint32(index) ||
			effect.Action == "" ||
			effect.Target == "" ||
			effect.ParametersDigest == "" ||
			!json.Valid(effect.Parameters) {
			return fmt.Errorf("effect %d is incomplete", index)
		}
		if effect.AdapterID == "" ||
			effect.ContractVersion == "" ||
			effect.KeyRetentionSeconds <= 0 ||
			effect.LookupConsistencyWindowSeconds < 0 {
			return fmt.Errorf("effect %d has no recoverable adapter binding", index)
		}
		if _, exists := seen[effect.ID]; exists {
			return fmt.Errorf("duplicate effect id %q", effect.ID)
		}
		seen[effect.ID] = struct{}{}
		digest, err := domain.DigestCanonicalJSON(effect.Parameters)
		if err != nil || digest != effect.ParametersDigest {
			return fmt.Errorf("effect %d parameters digest mismatch", index)
		}
	}
	return nil
}
