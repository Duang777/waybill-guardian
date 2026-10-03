package platform_test

import (
	"context"
	"testing"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform"
	"github.com/Duang777/waybill-guardian/internal/tools"
)

func TestMockCoversPlatformInterfaces(t *testing.T) {
	clients, mock, err := tools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	waybill, err := clients.TMS.GetWaybill(ctx, platform.GetWaybillRequest{WaybillID: "YD2026101001"})
	if err != nil {
		t.Fatal(err)
	}
	if waybill.Origin != "杭州" || waybill.Destination != "成都" || len(waybill.CandidateCarriers) != 2 {
		t.Fatalf("unexpected waybill: %+v", waybill)
	}
	points, err := clients.TMS.GetTracking(ctx, platform.GetTrackingRequest{WaybillID: waybill.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 6 || !points[4].Anomaly || points[4].StopHours != 6 {
		t.Fatalf("unexpected tracking: %+v", points)
	}
	driver, err := clients.TMS.GetDriver(ctx, platform.GetDriverRequest{DriverID: waybill.DriverID})
	if err != nil {
		t.Fatal(err)
	}
	if !driver.FatigueAlert || driver.ContinuousDriveHrs != 9 {
		t.Fatalf("unexpected driver: %+v", driver)
	}
	weather, err := clients.Weather.GetRoadWeather(ctx, platform.GetRoadWeatherRequest{Route: "杭州-成都"})
	if err != nil {
		t.Fatal(err)
	}
	if len(weather) != 1 || weather[0].AlertLevel != "none" {
		t.Fatalf("unexpected weather: %+v", weather)
	}

	key := domain.IdempotencyKey("reassign-key")
	request := platform.ReassignRequest{
		WaybillID: waybill.ID, CarrierID: waybill.CandidateCarriers[0].ID, IdempotencyKey: key,
	}
	first, err := clients.TMS.Reassign(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := clients.TMS.Reassign(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("same key returned different orders: %+v %+v", first, second)
	}
	if count := mock.WriteCount(domain.ActionReassign); count != 1 {
		t.Fatalf("reassign writes = %d, want 1", count)
	}
}
