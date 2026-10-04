package guardian

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform"
	"github.com/Duang777/waybill-guardian/internal/platform/filestore"
	"github.com/Duang777/waybill-guardian/internal/tools"
)

func TestArbitraryWaybillDrivesRunEvidenceAndProposal(t *testing.T) {
	loaded, err := filestore.LoadEmbeddedJSON("arbitrary.json", []byte(arbitraryDataset))
	if err != nil {
		t.Fatal(err)
	}
	writes, err := tools.NewFixtureWriteRuntime(loaded.Reads)
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(Config{
		DataDir:      t.TempDir(),
		Reads:        loaded.Reads,
		WriteRuntime: writes,
		StepDelay:    0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	catalog, err := service.ListWaybills(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 1 ||
		catalog[0].WaybillID != "YD2026101042" ||
		catalog[0].Origin != "宁波" ||
		catalog[0].Destination != "西安" ||
		catalog[0].AnomalyLabel != "襄阳服务区" {
		t.Fatalf("catalog = %+v", catalog)
	}

	run, err := service.StartRun(t.Context(), "YD2026101042")
	if err != nil {
		t.Fatal(err)
	}
	pending := waitForApproval(t, service, run.RunID)
	var carrierID string
	for _, item := range pending.Items {
		if item.Action != domain.ActionReassign {
			continue
		}
		var arguments struct {
			CarrierID string `json:"carrier_id"`
		}
		if err := json.Unmarshal(item.Params, &arguments); err != nil {
			t.Fatal(err)
		}
		carrierID = arguments.CarrierID
	}
	if carrierID != "CARRIER-NW-8" {
		t.Fatalf("proposed carrier = %q", carrierID)
	}
	evidence, err := json.Marshal(pending.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2.5 小时", "襄阳服务区停留3.5 小时", "yellow"} {
		if !strings.Contains(string(evidence), want) {
			t.Fatalf("approval evidence %s does not contain %q", evidence, want)
		}
	}
	for _, stale := range []string{"绵阳", "9 小时", "川行快运"} {
		if strings.Contains(string(evidence), stale) {
			t.Fatalf("approval evidence contains stale fixture value %q: %s", stale, evidence)
		}
	}

	view, err := service.GetWaybill(t.Context(), run.WaybillID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Waybill.Origin != "宁波" ||
		view.Waybill.Destination != "西安" ||
		view.Driver.ID != "DRIVER-NB-7" ||
		view.Risk.Weather != 30 ||
		view.Risk.ETADelay == 86 {
		t.Fatalf("waybill view = %+v", view)
	}

	events, err := service.Replay(t.Context(), run.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var attribution string
	for _, event := range events {
		if event.Type == audit.EventAttribution {
			attribution = string(event.Payload)
		}
	}
	if !strings.Contains(attribution, "襄阳服务区") ||
		!strings.Contains(attribution, "沿途天气预警") {
		t.Fatalf("attribution = %s", attribution)
	}
}

func TestStartRunRejectsUnknownWaybillBeforeCreatingAuditState(t *testing.T) {
	loaded, err := filestore.LoadEmbeddedJSON("arbitrary.json", []byte(arbitraryDataset))
	if err != nil {
		t.Fatal(err)
	}
	writes, err := tools.NewFixtureWriteRuntime(loaded.Reads)
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(Config{
		DataDir:      t.TempDir(),
		Reads:        loaded.Reads,
		WriteRuntime: writes,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	_, err = service.StartRun(context.Background(), "YD2026101099")
	if !errors.Is(err, platform.ErrNotFound) {
		t.Fatalf("StartRun error = %v, want platform.ErrNotFound", err)
	}
	events, err := service.journal.AllEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("unknown waybill created audit events: %+v", events)
	}
}

const arbitraryDataset = `{
  "schema_version": "v1",
  "dataset_id": "arbitrary-test",
  "waybills": [{
    "waybill_id": "YD2026101042",
    "origin": "宁波",
    "destination": "西安",
    "cargo": "医疗器械",
    "current_carrier_id": "CARRIER-EAST-9",
    "driver_id": "DRIVER-NB-7",
    "status": "delayed",
    "sla_hours": 36,
    "shipper_phone": "13700004321"
  }],
  "drivers": [{
    "driver_id": "DRIVER-NB-7",
    "name": "林师傅",
    "phone": "13600007890",
    "plate": "浙B7K2P1",
    "continuous_drive_hours": 2.5,
    "fatigue_alert": false
  }],
  "waybill_candidates": [{
    "waybill_id": "YD2026101042",
    "priority": 1,
    "carrier_id": "CARRIER-NW-8",
    "name": "秦岭联运",
    "eta_hours": 8,
    "reliability_pct": 98.2
  }],
  "tracking": [{
    "waybill_id": "YD2026101042",
    "sequence": 1,
    "label": "宁波枢纽",
    "recorded_at": "2026-10-10T01:00:00Z",
    "longitude": 121.5503,
    "latitude": 29.8746,
    "speed_kph": 55,
    "anomaly": false
  }, {
    "waybill_id": "YD2026101042",
    "sequence": 2,
    "label": "襄阳服务区",
    "recorded_at": "2026-10-10T08:00:00Z",
    "longitude": 112.1441,
    "latitude": 32.0424,
    "speed_kph": 0,
    "stop_hours": 3.5,
    "anomaly": true
  }],
  "weather": [{
    "origin": "宁波",
    "destination": "西安",
    "sequence": 1,
    "segment": "沪陕高速",
    "condition": "小雨",
    "alert_level": "yellow"
  }]
}`
