package execution

import (
	"context"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func TestPlannerBuildsDeterministicRecoverableEffectSet(t *testing.T) {
	adapter := &staticAdapter{capability: Capability{
		AdapterID:               "enterprise-delivery-v1",
		ContractVersion:         "v1",
		Actions:                 allEffectActions(),
		SameRequestReplays:      true,
		MismatchRejected:        true,
		LookupByKey:             true,
		KeyRetention:            72 * time.Hour,
		LookupConsistencyWindow: 30 * time.Second,
		SupportsRecovery:        true,
	}}
	registry, err := NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	planner := Planner{Registry: registry, RecoveryHorizon: 24 * time.Hour}
	plan := effectPlan()

	first, err := planner.BuildEffectSet("tenant-a", plan.RevisionID, plan)
	if err != nil {
		t.Fatal(err)
	}
	second, err := planner.BuildEffectSet("tenant-a", plan.RevisionID, plan)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest {
		t.Fatalf("effect digest changed: %s != %s", first.Digest, second.Digest)
	}
	if len(first.Effects) != 6 {
		t.Fatalf("effects = %d, want 6", len(first.Effects))
	}
	for index, effect := range first.Effects {
		if effect.Ordinal != uint32(index) ||
			effect.AdapterID != adapter.capability.AdapterID ||
			effect.ContractVersion != adapter.capability.ContractVersion ||
			effect.KeyRetentionSeconds != int64((72*time.Hour)/time.Second) {
			t.Fatalf("effect %d = %+v", index, effect)
		}
	}
	if err := VerifyEffectSet(first); err != nil {
		t.Fatal(err)
	}
}

func TestPlannerRejectsRequiredActionWithoutRecoverableAdapter(t *testing.T) {
	registry, err := NewRegistry(&staticAdapter{capability: Capability{
		AdapterID:               "notify-v1",
		ContractVersion:         "v1",
		Actions:                 []domain.EffectAction{domain.EffectNotifyETA},
		SameRequestReplays:      true,
		MismatchRejected:        true,
		LookupByKey:             true,
		KeyRetention:            72 * time.Hour,
		LookupConsistencyWindow: time.Second,
		SupportsRecovery:        true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (Planner{
		Registry:        registry,
		RecoveryHorizon: 24 * time.Hour,
	}).BuildEffectSet("tenant-a", "revision-1", effectPlan())
	if err == nil {
		t.Fatal("planner accepted a required action without an adapter")
	}
}

func TestPlannerRejectsOptionalActionWithoutRecoverableAdapter(t *testing.T) {
	registry, err := NewRegistry(&staticAdapter{capability: Capability{
		AdapterID:       "delivery-v1",
		ContractVersion: "v1",
		Actions: []domain.EffectAction{
			domain.EffectCreateRoute,
			domain.EffectAssignVehicle,
			domain.EffectAssignDriver,
			domain.EffectPublishStops,
			domain.EffectPublishLoad,
		},
		SameRequestReplays:      true,
		MismatchRejected:        true,
		LookupByKey:             true,
		KeyRetention:            72 * time.Hour,
		LookupConsistencyWindow: time.Second,
		SupportsRecovery:        true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (Planner{
		Registry:        registry,
		RecoveryHorizon: 24 * time.Hour,
	}).BuildEffectSet("tenant-a", "revision-1", effectPlan())
	if err == nil {
		t.Fatal("planner accepted an optional action without an adapter")
	}
}

type staticAdapter struct {
	capability Capability
}

func (adapter *staticAdapter) Capability() Capability {
	return adapter.capability
}

func (adapter *staticAdapter) Bind(
	preview domain.EffectPreview,
	key string,
	createdAt time.Time,
) (Binding, error) {
	return NewBinding(preview, key, createdAt, adapter.capability)
}

func (*staticAdapter) Dispatch(context.Context, Binding) (Result, error) {
	return Result{Disposition: DispositionSucceeded}, nil
}

func (*staticAdapter) Lookup(context.Context, Binding, time.Time) (Result, error) {
	return Result{Disposition: DispositionSucceeded}, nil
}

func allEffectActions() []domain.EffectAction {
	return []domain.EffectAction{
		domain.EffectCreateRoute,
		domain.EffectAssignVehicle,
		domain.EffectAssignDriver,
		domain.EffectPublishStops,
		domain.EffectPublishLoad,
		domain.EffectNotifyETA,
	}
}

func effectPlan() domain.Plan {
	base := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	return domain.Plan{
		SchemaVersion: domain.PlanSchemaVersion,
		PlanID:        "plan-1",
		RevisionID:    "revision-1",
		PlanDigest:    domain.ArtifactDigest("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		Duties: []domain.VehicleDuty{{
			VehicleID: "vehicle-1",
			DriverIDs: []domain.DriverID{"driver-1"},
			Trips: []domain.Trip{{
				ID:           "trip-1",
				StartDepotID: "depot-1",
				EndDepotID:   "depot-1",
				StartAt:      base,
				EndAt:        base.Add(time.Hour),
				Stops: []domain.Stop{{
					LocationID:  "customer-1",
					TaskIDs:     []domain.TaskID{"task-1"},
					ArrivalAt:   base.Add(20 * time.Minute),
					ServiceAt:   base.Add(25 * time.Minute),
					DepartureAt: base.Add(30 * time.Minute),
				}},
				Schedule:   []domain.DutySegment{},
				Energy:     []domain.EnergyLeg{},
				LoadStages: []domain.LoadStage{},
			}},
		}},
		Unassigned: []domain.UnassignedUnit{},
	}
}
