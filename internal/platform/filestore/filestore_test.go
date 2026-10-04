package filestore

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform"
)

func TestJSONAndCSVTemplatesProduceEquivalentSnapshots(t *testing.T) {
	jsonLoaded := loadTemplate(t, "waybills-v1.json")
	csvLoaded := loadTemplate(t, "waybills-v1.csv")

	if jsonLoaded.Source.Format != "json" || csvLoaded.Source.Format != "csv" {
		t.Fatalf(
			"formats = %q, %q; want json, csv",
			jsonLoaded.Source.Format,
			csvLoaded.Source.Format,
		)
	}
	if jsonLoaded.Source.DatasetID != "template-v1" ||
		csvLoaded.Source.DatasetID != "template-v1" {
		t.Fatalf("dataset IDs = %q, %q", jsonLoaded.Source.DatasetID, csvLoaded.Source.DatasetID)
	}
	if jsonLoaded.Stats != (Stats{Waybills: 1, Anomalies: 1}) ||
		csvLoaded.Stats != jsonLoaded.Stats {
		t.Fatalf("stats = %+v, %+v", jsonLoaded.Stats, csvLoaded.Stats)
	}
	if strings.Contains(jsonLoaded.Source.String(), string(filepath.Separator)) {
		t.Fatalf("source descriptor leaks a path: %q", jsonLoaded.Source.String())
	}
	if len(jsonLoaded.Source.Digest) != 64 ||
		len(csvLoaded.Source.Digest) != 64 {
		t.Fatalf(
			"source digests must be complete SHA-256 values: %q, %q",
			jsonLoaded.Source.Digest,
			csvLoaded.Source.Digest,
		)
	}

	jsonCase := readCase(t, jsonLoaded.Reads, "YD2026101001")
	csvCase := readCase(t, csvLoaded.Reads, "YD2026101001")
	if !reflect.DeepEqual(jsonCase, csvCase) {
		t.Fatalf("JSON and CSV snapshots differ:\nJSON=%+v\nCSV=%+v", jsonCase, csvCase)
	}
}

func TestSnapshotReturnsDefensiveCopies(t *testing.T) {
	loaded := loadTemplate(t, "waybills-v1.json")
	ctx := context.Background()
	id := domain.WaybillID("YD2026101001")

	waybill, err := loaded.Reads.TMS.GetWaybill(ctx, platform.GetWaybillRequest{
		WaybillID: id,
	})
	if err != nil {
		t.Fatal(err)
	}
	waybill.CandidateCarriers[0].Name = "mutated"
	points, err := loaded.Reads.TMS.GetTracking(ctx, platform.GetTrackingRequest{
		WaybillID: id,
	})
	if err != nil {
		t.Fatal(err)
	}
	points[0].Label = "mutated"
	weather, err := loaded.Reads.Weather.GetRoadWeather(
		ctx,
		platform.GetRoadWeatherRequest{Route: "杭州-成都"},
	)
	if err != nil {
		t.Fatal(err)
	}
	weather[0].Condition = "mutated"
	catalog, err := loaded.Reads.Catalog.ListWaybills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	catalog[0].Origin = "mutated"

	again := readCase(t, loaded.Reads, string(id))
	if again.Waybill.CandidateCarriers[0].Name != "川行快运" {
		t.Fatal("candidate carrier mutation changed the snapshot")
	}
	if again.Tracking[0].Label != "杭州转运中心" {
		t.Fatal("tracking mutation changed the snapshot")
	}
	if again.Weather[0].Condition != "晴" {
		t.Fatal("weather mutation changed the snapshot")
	}
	if again.Catalog[0].Origin != "杭州" {
		t.Fatal("catalog mutation changed the snapshot")
	}
}

func TestCatalogIsSortedByWaybillID(t *testing.T) {
	draft := validDraft()
	draft.Waybills[0], draft.Waybills[1] = draft.Waybills[1], draft.Waybills[0]
	raw, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadEmbeddedJSON("sorted.json", raw)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loaded.Reads.Catalog.ListWaybills(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 2 ||
		catalog[0].WaybillID != "YD2026101001" ||
		catalog[1].WaybillID != "YD2026101002" {
		t.Fatalf("catalog order = %+v", catalog)
	}
}

func TestNetworkExtensionBuildsTypedCatalog(t *testing.T) {
	draft := validDraft()
	draft.Hubs = []hubDraft{
		{
			HubID:         "HUB-HGH",
			Name:          "杭州公路港",
			Province:      "浙江",
			City:          "杭州",
			Longitude:     120.1551,
			Latitude:      30.2741,
			DailyCapacity: 2400,
		},
		{
			HubID:         "HUB-CTU",
			Name:          "成都公路港",
			Province:      "四川",
			City:          "成都",
			Longitude:     104.0665,
			Latitude:      30.5723,
			DailyCapacity: 2100,
		},
		{
			HubID:         "HUB-NGB",
			Name:          "宁波公路港",
			Province:      "浙江",
			City:          "宁波",
			Longitude:     121.5503,
			Latitude:      29.8746,
			DailyCapacity: 1900,
		},
		{
			HubID:         "HUB-XIY",
			Name:          "西安公路港",
			Province:      "陕西",
			City:          "西安",
			Longitude:     108.9398,
			Latitude:      34.3416,
			DailyCapacity: 1800,
		},
	}
	draft.Vehicles = []vehicleDraft{
		{
			VehicleID:        "VEHICLE-1",
			MaskedPlate:      "浙A****1",
			Type:             "厢式货车",
			LoadCapacityTons: 18,
		},
		{
			VehicleID:        "VEHICLE-2",
			MaskedPlate:      "浙B****2",
			Type:             "冷链货车",
			LoadCapacityTons: 12,
		},
	}
	draft.Routes = []routeDraft{
		{
			RouteID:          "ROUTE-HGH-CTU",
			OriginHubID:      "HUB-HGH",
			DestinationHubID: "HUB-CTU",
			DistanceKM:       1860,
			StandardHours:    31,
		},
		{
			RouteID:          "ROUTE-NGB-XIY",
			OriginHubID:      "HUB-NGB",
			DestinationHubID: "HUB-XIY",
			DistanceKM:       1510,
			StandardHours:    25,
		},
	}
	draft.Waybills[0].OriginHubID = "HUB-HGH"
	draft.Waybills[0].DestinationHubID = "HUB-CTU"
	draft.Waybills[0].RouteID = "ROUTE-HGH-CTU"
	draft.Waybills[0].VehicleID = "VEHICLE-1"
	draft.Waybills[1].OriginHubID = "HUB-NGB"
	draft.Waybills[1].DestinationHubID = "HUB-XIY"
	draft.Waybills[1].RouteID = "ROUTE-NGB-XIY"
	draft.Waybills[1].VehicleID = "VEHICLE-2"
	draft.Weather[0].RouteID = "ROUTE-HGH-CTU"
	draft.Weather[1].RouteID = "ROUTE-NGB-XIY"

	raw, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadEmbeddedJSON("network.json", raw)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Stats != (Stats{
		Waybills: 2,
		Hubs:     4,
		Vehicles: 2,
		Routes:   2,
	}) {
		t.Fatalf("stats = %+v", loaded.Stats)
	}
	hubs, err := loaded.Reads.Network.ListHubs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	routes, err := loaded.Reads.Network.ListRoutes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	vehicles, err := loaded.Reads.Network.ListVehicles(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(hubs) != 4 || hubs[0].ID != "HUB-CTU" {
		t.Fatalf("hubs = %+v", hubs)
	}
	if len(routes) != 2 || routes[0].ID != "ROUTE-HGH-CTU" {
		t.Fatalf("routes = %+v", routes)
	}
	if len(vehicles) != 2 || vehicles[0].ID != "VEHICLE-1" {
		t.Fatalf("vehicles = %+v", vehicles)
	}
	waybill, err := loaded.Reads.TMS.GetWaybill(
		t.Context(),
		platform.GetWaybillRequest{WaybillID: "YD2026101001"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if waybill.OriginHubID != "HUB-HGH" ||
		waybill.DestinationHubID != "HUB-CTU" ||
		waybill.RouteID != "ROUTE-HGH-CTU" ||
		waybill.VehicleID != "VEHICLE-1" {
		t.Fatalf("network references = %+v", waybill)
	}
}

func TestNetworkExtensionRejectsBrokenReferences(t *testing.T) {
	draft := validDraft()
	draft.Hubs = []hubDraft{{
		HubID:         "HUB-HGH",
		Name:          "杭州公路港",
		Province:      "浙江",
		City:          "杭州",
		Longitude:     120.1551,
		Latitude:      30.2741,
		DailyCapacity: 2400,
	}}
	draft.Vehicles = []vehicleDraft{{
		VehicleID:        "VEHICLE-1",
		MaskedPlate:      "浙A****1",
		Type:             "厢式货车",
		LoadCapacityTons: 18,
	}}
	draft.Routes = []routeDraft{{
		RouteID:          "ROUTE-HGH-MISSING",
		OriginHubID:      "HUB-HGH",
		DestinationHubID: "HUB-MISSING",
		DistanceKM:       100,
		StandardHours:    2,
	}}
	for index := range draft.Waybills {
		draft.Waybills[index].OriginHubID = "HUB-HGH"
		draft.Waybills[index].DestinationHubID = "HUB-MISSING"
		draft.Waybills[index].RouteID = "ROUTE-HGH-MISSING"
		draft.Waybills[index].VehicleID = "VEHICLE-1"
	}
	for index := range draft.Weather {
		draft.Weather[index].RouteID = "ROUTE-HGH-MISSING"
	}

	raw, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LoadEmbeddedJSON("invalid-network.json", raw)
	if err == nil ||
		!strings.Contains(err.Error(), "destination_hub_id: references an unknown hub") {
		t.Fatalf("error = %v", err)
	}
}

func TestCSVNetworkExtension(t *testing.T) {
	records := []map[string]string{
		{
			"record_type":    "dataset",
			"schema_version": "v1",
			"dataset_id":     "network-csv",
		},
		{
			"record_type": "hub", "hub_id": "HUB-1", "hub_name": "南京公路港",
			"province": "江苏", "city": "南京", "longitude": "118.7969",
			"latitude": "32.0603", "daily_capacity": "1800",
		},
		{
			"record_type": "hub", "hub_id": "HUB-2", "hub_name": "青岛公路港",
			"province": "山东", "city": "青岛", "longitude": "120.3826",
			"latitude": "36.0671", "daily_capacity": "1700",
		},
		{
			"record_type": "vehicle", "vehicle_id": "VEHICLE-1",
			"vehicle_plate": "苏A****1", "vehicle_type": "厢式货车",
			"load_capacity_tons": "18",
		},
		{
			"record_type": "route", "route_id": "ROUTE-1",
			"origin_hub_id": "HUB-1", "destination_hub_id": "HUB-2",
			"distance_km": "570", "standard_hours": "10",
		},
		{
			"record_type": "waybill", "waybill_id": "YD2026101041",
			"origin": "南京", "destination": "青岛", "origin_hub_id": "HUB-1",
			"destination_hub_id": "HUB-2", "route_id": "ROUTE-1",
			"vehicle_id": "VEHICLE-1", "cargo": "医疗器械",
			"current_carrier_id": "CARRIER-1", "driver_id": "DRIVER-1",
			"status": "delay", "sla_hours": "18", "shipper_phone": "13800004101",
		},
		{
			"record_type": "driver", "driver_id": "DRIVER-1",
			"driver_name": "顾师傅", "driver_phone": "13900004101",
			"driver_plate": "苏A4Q101", "continuous_drive_hours": "7.5",
			"fatigue_alert": "false",
		},
		{
			"record_type": "candidate", "waybill_id": "YD2026101041",
			"priority": "1", "carrier_id": "CARRIER-2", "carrier_name": "海岱物流",
			"eta_hours": "9", "reliability_pct": "96.8",
		},
		{
			"record_type": "tracking", "waybill_id": "YD2026101041",
			"tracking_sequence": "1", "label": "南京公路港",
			"recorded_at": "2026-10-10T01:00:00Z", "longitude": "118.7969",
			"latitude": "32.0603", "speed_kph": "0", "anomaly": "false",
		},
		{
			"record_type": "tracking", "waybill_id": "YD2026101041",
			"tracking_sequence": "2", "label": "临沂服务区",
			"recorded_at": "2026-10-10T06:30:00Z", "longitude": "118.3564",
			"latitude": "35.1047", "speed_kph": "0", "stop_hours": "3",
			"anomaly": "true", "anomaly_type": "delay",
		},
		{
			"record_type": "weather", "origin": "南京", "destination": "青岛",
			"route_id": "ROUTE-1", "weather_sequence": "1",
			"segment": "沈海高速", "condition": "多云", "alert_level": "none",
		},
	}
	var raw bytes.Buffer
	writer := csv.NewWriter(&raw)
	if err := writer.Write(csvColumns); err != nil {
		t.Fatal(err)
	}
	for _, values := range records {
		record := make([]string, len(csvColumns))
		for index, field := range csvColumns {
			record[index] = values[field]
		}
		if err := writer.Write(record); err != nil {
			t.Fatal(err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadBytes("csv", raw.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Stats != (Stats{
		Waybills:  1,
		Anomalies: 1,
		Hubs:      2,
		Vehicles:  1,
		Routes:    1,
	}) {
		t.Fatalf("stats = %+v", loaded.Stats)
	}
}

func TestValidationRejectsInvalidDataWithStableLocations(t *testing.T) {
	template := readTemplate(t, "waybills-v1.csv")
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "missing header",
			raw: strings.Replace(
				template,
				",driver_id,status",
				",status",
				1,
			),
			want: "header driver_id: missing required column",
		},
		{
			name: "unrelated field",
			raw: strings.Replace(
				template,
				"v1,template-v1,dataset,,",
				"v1,template-v1,dataset,YD2026101099,",
				1,
			),
			want: "row 2 waybill_id: must be empty for this record type",
		},
		{
			name: "longitude",
			raw:  strings.Replace(template, "120.1551", "181", 1),
			want: "row 7 longitude: must be between -180 and 180",
		},
		{
			name: "time order",
			raw: strings.Replace(
				template,
				"2026-10-11T04:21:00Z",
				"2026-10-09T04:21:00Z",
				1,
			),
			want: "row 8 recorded_at: must be later than the previous point",
		},
		{
			name: "surrounding ID whitespace",
			raw: strings.Replace(
				template,
				"CARRIER-SW-42",
				" CARRIER-SW-42",
				1,
			),
			want: "row 5 carrier_id: must not have leading or trailing whitespace",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "input.csv")
			if err := os.WriteFile(path, []byte(test.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("error type = %T, want *ValidationError", err)
			}
		})
	}
}

func TestJSONParserRejectsUnknownDuplicateAndTrailingFields(t *testing.T) {
	valid := readTemplate(t, "waybills-v1.json")
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "unknown",
			raw: strings.Replace(
				valid,
				`"dataset_id": "template-v1",`,
				`"dataset_id": "template-v1", "unknown": true,`,
				1,
			),
			want: `json: json: unknown field "unknown"`,
		},
		{
			name: "duplicate",
			raw: strings.Replace(
				valid,
				`"dataset_id": "template-v1",`,
				`"dataset_id": "template-v1", "dataset_id": "other",`,
				1,
			),
			want: "json dataset_id: duplicate top-level field",
		},
		{
			name: "nested duplicate",
			raw: strings.Replace(
				valid,
				`"origin": "杭州",`,
				`"origin": "杭州", "origin": "南京",`,
				1,
			),
			want: "json.waybills[0] origin: duplicate field",
		},
		{
			name: "missing required scalar",
			raw: strings.Replace(
				valid,
				`"sla_hours": 72,`,
				"",
				1,
			),
			want: "waybills[0] sla_hours: is required",
		},
		{
			name: "null required boolean",
			raw: strings.Replace(
				valid,
				`"fatigue_alert": true`,
				`"fatigue_alert": null`,
				1,
			),
			want: "drivers[0] fatigue_alert: is required",
		},
		{
			name: "trailing",
			raw:  valid + "\n{}",
			want: "json: trailing value is not allowed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := LoadEmbeddedJSON("input.json", []byte(test.raw))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestSnapshotSupportsConcurrentReadsAndCancellation(t *testing.T) {
	loaded := loadTemplate(t, "waybills-v1.json")
	const readers = 32
	const iterations = 100
	failures := make(chan error, readers)
	var wait sync.WaitGroup
	wait.Add(readers)
	for range readers {
		go func() {
			defer wait.Done()
			for range iterations {
				result, err := readCaseValue(loaded.Reads, "YD2026101001")
				if err != nil {
					failures <- err
					return
				}
				if result.Catalog[0].WaybillID != "YD2026101001" {
					failures <- errors.New("unexpected catalog item")
					return
				}
			}
		}()
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := loaded.Reads.Catalog.ListWaybills(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read error = %v", err)
	}
}

func TestLoadDoesNotExposeAbsolutePathOrSensitiveValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-input.csv")
	if err := os.WriteFile(path, []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load succeeded for an invalid header")
	}
	if strings.Contains(err.Error(), filepath.Dir(path)) {
		t.Fatalf("error leaks absolute path: %v", err)
	}

	raw := strings.Replace(
		readTemplate(t, "waybills-v1.json"),
		`"phone": "13961234567"`,
		`"phone": ""`,
		1,
	)
	_, err = LoadEmbeddedJSON("sensitive.json", []byte(raw))
	if err == nil {
		t.Fatal("LoadEmbeddedJSON succeeded with an empty phone")
	}
	for _, sensitive := range []string{"13961234567", "川A8X6Q2"} {
		if strings.Contains(err.Error(), sensitive) {
			t.Fatalf("error leaks sensitive value %q: %v", sensitive, err)
		}
	}
}

type loadedCase struct {
	Waybill  platform.Waybill
	Tracking []platform.TrackPoint
	Driver   platform.Driver
	Weather  []platform.RoadWeather
	Catalog  []platform.WaybillSummary
}

func readCase(t *testing.T, reads platform.ReadSet, waybillID string) loadedCase {
	t.Helper()
	result, err := readCaseValue(reads, waybillID)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func readCaseValue(reads platform.ReadSet, waybillID string) (loadedCase, error) {
	ctx := context.Background()
	waybill, err := reads.TMS.GetWaybill(ctx, platform.GetWaybillRequest{
		WaybillID: domain.WaybillID(waybillID),
	})
	if err != nil {
		return loadedCase{}, err
	}
	tracking, err := reads.TMS.GetTracking(ctx, platform.GetTrackingRequest{
		WaybillID: waybill.ID,
	})
	if err != nil {
		return loadedCase{}, err
	}
	driver, err := reads.TMS.GetDriver(ctx, platform.GetDriverRequest{
		DriverID: waybill.DriverID,
	})
	if err != nil {
		return loadedCase{}, err
	}
	weather, err := reads.Weather.GetRoadWeather(
		ctx,
		platform.GetRoadWeatherRequest{
			Route: waybill.Origin + "-" + waybill.Destination,
		},
	)
	if err != nil {
		return loadedCase{}, err
	}
	catalog, err := reads.Catalog.ListWaybills(ctx)
	if err != nil {
		return loadedCase{}, err
	}
	return loadedCase{
		Waybill:  waybill,
		Tracking: tracking,
		Driver:   driver,
		Weather:  weather,
		Catalog:  catalog,
	}, nil
}

func loadTemplate(t *testing.T, name string) Loaded {
	t.Helper()
	loaded, err := Load(templatePath(name))
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func readTemplate(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(templatePath(name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func templatePath(name string) string {
	return filepath.Join("..", "..", "..", "data", "templates", name)
}

func validDraft() datasetDraft {
	return datasetDraft{
		SchemaVersion: schemaVersionV1,
		DatasetID:     "sorting",
		Waybills: []waybillDraft{
			{
				WaybillID:        "YD2026101001",
				Origin:           "杭州",
				Destination:      "成都",
				Cargo:            "A",
				CurrentCarrierID: "CARRIER-1",
				DriverID:         "DRIVER-1",
				Status:           "delayed",
				SLAHours:         1,
				ShipperPhone:     "10000000001",
			},
			{
				WaybillID:        "YD2026101002",
				Origin:           "宁波",
				Destination:      "西安",
				Cargo:            "B",
				CurrentCarrierID: "CARRIER-2",
				DriverID:         "DRIVER-2",
				Status:           "in_transit",
				SLAHours:         2,
				ShipperPhone:     "10000000002",
			},
		},
		Drivers: []driverDraft{
			{
				DriverID:             "DRIVER-1",
				Name:                 "A",
				Phone:                "10000000001",
				Plate:                "A1",
				ContinuousDriveHours: 1,
			},
			{
				DriverID:             "DRIVER-2",
				Name:                 "B",
				Phone:                "10000000002",
				Plate:                "B2",
				ContinuousDriveHours: 2,
			},
		},
		WaybillCandidates: []candidateDraft{
			{
				WaybillID:      "YD2026101001",
				Priority:       1,
				CarrierID:      "NEXT-1",
				Name:           "Next A",
				ETAHours:       1,
				ReliabilityPct: 99,
			},
			{
				WaybillID:      "YD2026101002",
				Priority:       1,
				CarrierID:      "NEXT-2",
				Name:           "Next B",
				ETAHours:       2,
				ReliabilityPct: 98,
			},
		},
		Tracking: []trackingDraft{
			{
				WaybillID:  "YD2026101001",
				Sequence:   1,
				Label:      "A",
				RecordedAt: "2026-10-10T01:00:00Z",
				Longitude:  120,
				Latitude:   30,
			},
			{
				WaybillID:  "YD2026101002",
				Sequence:   1,
				Label:      "B",
				RecordedAt: "2026-10-10T02:00:00Z",
				Longitude:  110,
				Latitude:   31,
			},
		},
		Weather: []weatherDraft{
			{
				Origin:      "杭州",
				Destination: "成都",
				Sequence:    1,
				Segment:     "A",
				Condition:   "clear",
				AlertLevel:  "none",
			},
			{
				Origin:      "宁波",
				Destination: "西安",
				Sequence:    1,
				Segment:     "B",
				Condition:   "clear",
				AlertLevel:  "none",
			},
		},
	}
}
