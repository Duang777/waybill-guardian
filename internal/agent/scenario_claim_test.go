package agent

import (
	"encoding/json"
	"testing"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform/filestore"
	guardtools "github.com/Duang777/waybill-guardian/internal/tools"
	"github.com/hastekit/agent-sdk-go/pkg/agents/agentstate"
)

func TestRequiredClaimTypesUsesDistinctDamageAndLossAnomalies(t *testing.T) {
	tracking := guardtools.GetTrackingOutput{Points: []guardtools.TrackingEvidence{
		{Anomaly: true, AnomalyType: "delay"},
		{Anomaly: true, AnomalyType: "damage"},
		{Anomaly: true, AnomalyType: "damage"},
		{Anomaly: false, AnomalyType: "loss"},
		{Anomaly: true, AnomalyType: "loss"},
	}}

	got := requiredClaimTypes(tracking)
	if len(got) != 2 || got[0] != claimTypeDamage || got[1] != claimTypeLoss {
		t.Fatalf("required claim types = %v, want [damage loss]", got)
	}
}

func TestScenarioModelAddsEvidenceDrivenClaimToApprovalBatch(t *testing.T) {
	loaded, err := filestore.Load("../../data/simulated/waybills-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := guardtools.NewHandlers(loaded.Reads)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := guardtools.NewRegistry(handlers)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(t.TempDir()+"/history", registry, nil, 0, ModelConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	tests := []struct {
		waybillID domain.WaybillID
		claimType claimType
		writes    int
	}{
		{waybillID: "YD2026100001", writes: 3},
		{waybillID: "YD2026100007", claimType: claimTypeDamage, writes: 4},
		{waybillID: "YD2026100013", claimType: claimTypeLoss, writes: 4},
	}
	for _, test := range tests {
		t.Run(string(test.waybillID), func(t *testing.T) {
			outcome, err := engine.Start(t.Context(), domain.RunContext{
				RunID:       domain.RunID("run-" + string(test.waybillID)),
				IncidentID:  domain.IncidentID("incident-" + string(test.waybillID)),
				WaybillID:   test.waybillID,
				PlanVersion: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			if outcome.Status != agentstate.RunStatusPaused {
				t.Fatalf("status = %q, want paused", outcome.Status)
			}
			if len(outcome.Interrupts) != test.writes {
				t.Fatalf("interrupts = %d, want %d", len(outcome.Interrupts), test.writes)
			}
			var claims []guardtools.CreateClaimInput
			for _, interrupt := range outcome.Interrupts {
				if interrupt.Action != domain.ActionCreateClaim {
					continue
				}
				var input guardtools.CreateClaimInput
				if err := json.Unmarshal(interrupt.Arguments, &input); err != nil {
					t.Fatal(err)
				}
				claims = append(claims, input)
			}
			if test.claimType == "" {
				if len(claims) != 0 {
					t.Fatalf("claims = %+v, want none", claims)
				}
				return
			}
			if len(claims) != 1 ||
				claims[0].WaybillID != string(test.waybillID) ||
				claims[0].ClaimType != string(test.claimType) {
				t.Fatalf("claims = %+v", claims)
			}
		})
	}
}
