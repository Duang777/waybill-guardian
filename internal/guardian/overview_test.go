package guardian

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform/filestore"
)

func TestOverviewAggregatesNetworkAndRanksAnomalies(t *testing.T) {
	service, ids := openSimulatedOverviewService(t)

	overview, err := service.Overview(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if overview.DataMode != "simulated" || !overview.NetworkAvailable {
		t.Fatalf("data mode/network = %q/%t", overview.DataMode, overview.NetworkAvailable)
	}
	if overview.Totals.Waybills != 200 ||
		overview.Totals.Anomalies != 67 ||
		len(overview.Hubs) != 72 ||
		len(overview.Routes) != 72 {
		t.Fatalf(
			"overview counts = waybills:%d anomalies:%d hubs:%d routes:%d",
			overview.Totals.Waybills,
			overview.Totals.Anomalies,
			len(overview.Hubs),
			len(overview.Routes),
		)
	}
	if len(overview.Anomalies) != 67 {
		t.Fatalf("anomaly queue = %d, want 67", len(overview.Anomalies))
	}
	for index := 1; index < len(overview.Anomalies); index++ {
		if overview.Anomalies[index-1].RiskScore < overview.Anomalies[index].RiskScore {
			t.Fatalf("anomaly queue is not risk sorted at index %d", index)
		}
	}
	distributed := 0
	for _, item := range overview.AnomalyDistribution {
		distributed += item.Count
	}
	if distributed != overview.Totals.Anomalies {
		t.Fatalf("anomaly distribution = %d, want %d", distributed, overview.Totals.Anomalies)
	}
	if len(overview.Brief.Items) != 3 {
		t.Fatalf("brief items = %d, want 3", len(overview.Brief.Items))
	}
	for _, item := range overview.Brief.Items {
		if len(item.Evidence) == 0 || item.Evidence[0].Source == "" {
			t.Fatalf("brief item has no citation: %+v", item)
		}
	}
}

func TestOverviewOnlyAggregatesRequestedWaybillScope(t *testing.T) {
	service, ids := openSimulatedOverviewService(t)
	scope := ids[:5]

	overview, err := service.Overview(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if overview.Totals.Waybills != len(scope) {
		t.Fatalf("waybills = %d, want %d", overview.Totals.Waybills, len(scope))
	}
	for _, item := range overview.Anomalies {
		found := false
		for _, allowed := range scope {
			if item.WaybillID == allowed {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("overview leaked waybill %q", item.WaybillID)
		}
	}
}

func TestKPIsExposeFormulasWithoutInventingMissingValueData(t *testing.T) {
	service, ids := openSimulatedOverviewService(t)

	report, err := service.KPIs(context.Background(), ids, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if report.Window != "24h0m0s" {
		t.Fatalf("window = %q", report.Window)
	}
	if report.Assumptions.EvidenceStepMinutes != 8 {
		t.Fatalf("evidence step minutes = %v", report.Assumptions.EvidenceStepMinutes)
	}
	if len(report.Metrics) < 4 {
		t.Fatalf("metrics = %d, want at least 4", len(report.Metrics))
	}
	metrics := make(map[string]KPIMetric, len(report.Metrics))
	for _, metric := range report.Metrics {
		if metric.Formula == "" {
			t.Fatalf("metric %q has no formula", metric.Key)
		}
		metrics[metric.Key] = metric
	}
	for _, key := range []string{"time_recovered_hours", "cost_impact_cny"} {
		metric := metrics[key]
		if metric.Availability != "unavailable" || metric.Value != nil || metric.Reason == "" {
			t.Fatalf("metric %q invented a value: %+v", key, metric)
		}
	}
	for _, key := range []string{"labor_saved_hours", "anomaly_closure_rate_pct"} {
		metric := metrics[key]
		if metric.Availability != "available" || metric.Value == nil {
			t.Fatalf("metric %q is unavailable: %+v", key, metric)
		}
	}
}

func TestKPIHelpersUseDocumentedArithmetic(t *testing.T) {
	if value := compositeRisk(RiskScore{ETADelay: 80, Road: 50, Weather: 20}); value != 59 {
		t.Fatalf("composite risk = %d, want 59", value)
	}
	if value := percentValue(2, 3); value < 66.66 || value > 66.67 {
		t.Fatalf("percentage = %v", value)
	}
	duration := durationMetric([]time.Duration{10 * time.Minute, 20 * time.Minute})
	if duration.Value == nil || *duration.Value != 15 {
		t.Fatalf("average duration = %+v", duration)
	}
	ratio := ratioMetric(3, 4)
	if ratio.Value == nil || *ratio.Value != 75 {
		t.Fatalf("approval ratio = %+v", ratio)
	}
}

func openSimulatedOverviewService(t *testing.T) (*Service, []domain.WaybillID) {
	t.Helper()
	loaded, err := filestore.Load(filepath.Join("..", "..", "data", "simulated", "waybills-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(Config{
		DataDir:             t.TempDir(),
		Reads:               loaded.Reads,
		ReadSource:          loaded.Source.String(),
		EvidenceStepMinutes: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("close service: %v", err)
		}
	})
	catalog, err := loaded.Reads.Catalog.ListWaybills(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]domain.WaybillID, 0, len(catalog))
	for _, item := range catalog {
		ids = append(ids, item.WaybillID)
	}
	return service, ids
}
