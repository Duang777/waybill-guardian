package filestore

import (
	"context"
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
