package main

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/platform/filestore"
)

func TestRunGeneratesValidatedNetworkDataset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "simulated.json")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(
		[]string{"--output", path, "--waybills", "200"},
		&stdout,
		&stderr,
	); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	loaded, err := filestore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Stats.Waybills != 200 ||
		loaded.Stats.Anomalies != 67 ||
		loaded.Stats.Hubs != 72 ||
		loaded.Stats.Vehicles != 200 ||
		loaded.Stats.Routes != 72 {
		t.Fatalf("stats = %+v", loaded.Stats)
	}
	if stdout.String() != "generated dataset=simulated-network-v1 hubs=72 routes=72 waybills=200 output=simulated.json\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	generated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile(
		filepath.Join("..", "..", "data", "simulated", "waybills-v1.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, committed) {
		t.Fatal("committed simulated dataset does not match the generator")
	}
}

func TestRunRejectsTooFewWaybills(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(
		[]string{"--output", "ignored.json", "--waybills", "71"},
		&stdout,
		&stderr,
	); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestGenerateBuildsDetailedNonLinearTrackingRoutes(t *testing.T) {
	first := generate(200)
	second := generate(200)
	if !reflect.DeepEqual(first.Tracking, second.Tracking) {
		t.Fatal("tracking generation is not deterministic")
	}

	hubs := make(map[string]hubRecord, len(first.Hubs))
	for _, hub := range first.Hubs {
		hubs[hub.HubID] = hub
	}
	tracking := make(map[string][]trackingRecord, len(first.Waybills))
	for _, point := range first.Tracking {
		tracking[point.WaybillID] = append(tracking[point.WaybillID], point)
	}

	for _, waybill := range first.Waybills {
		points := tracking[waybill.WaybillID]
		if len(points) != 7 {
			t.Fatalf("%s tracking points = %d, want 7", waybill.WaybillID, len(points))
		}
		origin := hubs[waybill.OriginHubID]
		destination := hubs[waybill.DestinationHubID]
		if points[0].Longitude != origin.Longitude ||
			points[0].Latitude != origin.Latitude {
			t.Fatalf("%s first point = (%f, %f), want origin (%f, %f)",
				waybill.WaybillID,
				points[0].Longitude,
				points[0].Latitude,
				origin.Longitude,
				origin.Latitude,
			)
		}
		last := points[len(points)-1]
		if last.Longitude != destination.Longitude ||
			last.Latitude != destination.Latitude {
			t.Fatalf("%s last point = (%f, %f), want destination (%f, %f)",
				waybill.WaybillID,
				last.Longitude,
				last.Latitude,
				destination.Longitude,
				destination.Latitude,
			)
		}
		if trackingIsLinear(points) {
			t.Fatalf("%s tracking points are collinear", waybill.WaybillID)
		}

		anomalies := 0
		anomalyIndex := -1
		for index, point := range points {
			if point.Sequence != index+1 {
				t.Fatalf("%s sequence[%d] = %d", waybill.WaybillID, index, point.Sequence)
			}
			if point.Anomaly {
				anomalies++
				anomalyIndex = index
			}
		}
		wantAnomalies := 0
		if waybill.Status != "in_transit" {
			wantAnomalies = 1
		}
		if anomalies != wantAnomalies {
			t.Fatalf("%s anomaly points = %d, want %d", waybill.WaybillID, anomalies, wantAnomalies)
		}
		if anomalyIndex >= 0 {
			anomaly := points[anomalyIndex]
			if anomaly.StopHours == nil {
				t.Fatalf("%s anomaly has no stop duration", waybill.WaybillID)
			}
			anomalyTime, err := time.Parse(time.RFC3339, anomaly.RecordedAt)
			if err != nil {
				t.Fatal(err)
			}
			for _, point := range points[anomalyIndex+1:] {
				if point.SpeedKPH != 0 {
					t.Fatalf(
						"%s resumed at %s with speed %d after anomaly",
						waybill.WaybillID,
						point.Label,
						point.SpeedKPH,
					)
				}
			}
			lastTime, err := time.Parse(time.RFC3339, last.RecordedAt)
			if err != nil {
				t.Fatal(err)
			}
			minimumArrival := anomalyTime.Add(
				time.Duration(*anomaly.StopHours * float64(time.Hour)),
			)
			if lastTime.Before(minimumArrival) {
				t.Fatalf(
					"%s arrival = %s, want no earlier than %s",
					waybill.WaybillID,
					lastTime,
					minimumArrival,
				)
			}
		}
	}
}

func TestBuildRouteCoordinatesFollowsPopulatedInlandCorridor(t *testing.T) {
	origin := hubRecord{
		Name:      "沈阳公路港",
		City:      "沈阳",
		Longitude: 123.4315,
		Latitude:  41.8057,
	}
	destination := hubRecord{
		Name:      "南昌公路港",
		City:      "南昌",
		Longitude: 115.8582,
		Latitude:  28.6829,
	}

	coordinates := buildRouteCoordinates(origin, destination, 6)
	midpoint := coordinates[len(coordinates)/2]
	directMidpointLongitude := (origin.Longitude + destination.Longitude) / 2

	if midpoint.Longitude >= directMidpointLongitude {
		t.Fatalf(
			"route midpoint longitude = %f, want west of direct midpoint %f",
			midpoint.Longitude,
			directMidpointLongitude,
		)
	}
}

func TestBuildRouteCoordinatesHandlesCoincidentEndpoints(t *testing.T) {
	hub := hubRecord{
		Name:      "同城公路港",
		City:      "杭州",
		Longitude: 120.1551,
		Latitude:  30.2741,
	}

	coordinates := buildRouteCoordinates(hub, hub, 3)

	if len(coordinates) != trackingPointCount {
		t.Fatalf("coordinates = %d, want %d", len(coordinates), trackingPointCount)
	}
	for index, coordinate := range coordinates {
		if math.IsNaN(coordinate.Longitude) ||
			math.IsInf(coordinate.Longitude, 0) ||
			math.IsNaN(coordinate.Latitude) ||
			math.IsInf(coordinate.Latitude, 0) {
			t.Fatalf("coordinate[%d] is not finite: %+v", index, coordinate)
		}
	}
}

func trackingIsLinear(points []trackingRecord) bool {
	start := points[0]
	end := points[len(points)-1]
	longitudeDelta := end.Longitude - start.Longitude
	latitudeDelta := end.Latitude - start.Latitude
	for _, point := range points[1 : len(points)-1] {
		crossProduct := (point.Longitude-start.Longitude)*latitudeDelta -
			(point.Latitude-start.Latitude)*longitudeDelta
		if math.Abs(crossProduct) > 0.000_001 {
			return false
		}
	}
	return true
}
