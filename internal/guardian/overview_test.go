package guardian

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentkit "github.com/Duang777/waybill-guardian/internal/agent"
	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform"
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

func TestOverviewUsesReadOnlyModelBriefWithServerOwnedCitations(t *testing.T) {
	generator := &briefGeneratorStub{
		result: agentkit.GeneratedBrief{Items: []agentkit.GeneratedBriefItem{
			{
				Headline:    "治理高频异常",
				Body:        "建议复盘共性",
				EvidenceIDs: []string{agentkit.EvidenceAnomalyMix},
			},
			{
				Headline:    "聚焦线路风险",
				Body:        "建议前置运力",
				EvidenceIDs: []string{agentkit.EvidenceRouteHotspot},
			},
			{
				Headline:    "平衡节点资源",
				Body:        "建议调整排班",
				EvidenceIDs: []string{agentkit.EvidenceHubPressure, agentkit.EvidenceFleetScope},
			},
		}},
	}
	service, ids := openSimulatedOverviewServiceWithBrief(t, generator)

	overview, err := service.Overview(t.Context(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if generator.calls != 1 {
		t.Fatalf("generator calls = %d, want 1", generator.calls)
	}
	if overview.Brief.Mode != "model_read_only" || len(overview.Brief.Items) != 3 {
		t.Fatalf("brief = %+v", overview.Brief)
	}
	if overview.Brief.Source != "model" || overview.Brief.FallbackReason != "" {
		t.Fatalf("brief source = %+v", overview.Brief)
	}
	wantIDs := []string{"model-brief-1", "model-brief-2", "model-brief-3"}
	for index, item := range overview.Brief.Items {
		if item.ID != wantIDs[index] {
			t.Fatalf("item ID = %q", item.ID)
		}
		if len(item.Evidence) == 0 {
			t.Fatalf("item has no evidence: %+v", item)
		}
		for _, evidence := range item.Evidence {
			if evidence.Label == "" || evidence.Value == "" || evidence.Source == "" {
				t.Fatalf("server citation is incomplete: %+v", evidence)
			}
		}
	}
}

func TestOverviewFallsBackWhenBriefGenerationFails(t *testing.T) {
	generator := &briefGeneratorStub{err: errors.New("provider unavailable")}
	service, ids := openSimulatedOverviewServiceWithBrief(t, generator)

	overview, err := service.Overview(t.Context(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if overview.Brief.Mode != "deterministic_read_only" ||
		len(overview.Brief.Items) != 3 {
		t.Fatalf("fallback brief = %+v", overview.Brief)
	}
	if overview.Brief.Source != "rules" ||
		overview.Brief.FallbackReason != BriefFallbackProvider {
		t.Fatalf("fallback metadata = %+v", overview.Brief)
	}
}

func TestOverviewClassifiesBriefFallbackReasons(t *testing.T) {
	tests := []struct {
		name      string
		generator *briefGeneratorStub
		want      BriefFallbackReason
	}{
		{
			name:      "timeout",
			generator: &briefGeneratorStub{err: context.DeadlineExceeded},
			want:      BriefFallbackTimeout,
		},
		{
			name: "schema",
			generator: &briefGeneratorStub{result: agentkit.GeneratedBrief{
				Items: []agentkit.GeneratedBriefItem{{Headline: "only one", Body: "invalid"}},
			}},
			want: BriefFallbackSchema,
		},
		{
			name: "evidence",
			generator: &briefGeneratorStub{result: agentkit.GeneratedBrief{
				Items: []agentkit.GeneratedBriefItem{
					{Headline: "一", Body: "一", EvidenceIDs: []string{"unknown"}},
					{Headline: "二", Body: "二", EvidenceIDs: []string{"unknown"}},
					{Headline: "三", Body: "三", EvidenceIDs: []string{"unknown"}},
				},
			}},
			want: BriefFallbackEvidence,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, ids := openSimulatedOverviewServiceWithBrief(t, test.generator)
			overview, err := service.Overview(t.Context(), ids)
			if err != nil {
				t.Fatal(err)
			}
			if overview.Brief.FallbackReason != test.want ||
				overview.Brief.Source != "rules" {
				t.Fatalf("fallback = %+v, want %q", overview.Brief, test.want)
			}
		})
	}
}

func TestOverviewUsesOnlineBriefAfterThreeSecondProviderResponse(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return
		case <-timer.C:
		}
		writeOnlineBriefResponse(w)
	}))
	defer provider.Close()

	service, ids := openSimulatedOverviewServiceWithModel(t, agentkit.ModelConfig{
		Mode:    agentkit.ModeOnline,
		BaseURL: provider.URL,
		APIKey:  "test-key",
		Model:   "brief-fake",
	})
	overview, err := service.Overview(t.Context(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if overview.Brief.Mode != "model_read_only" ||
		overview.Brief.Source != "brief-fake" ||
		overview.Brief.FallbackReason != "" {
		t.Fatalf("brief = %+v", overview.Brief)
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", calls.Load())
	}
}

func TestOverviewOnlineBriefTimeoutWarnsAndReturnsFallbackReason(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return
		case <-timer.C:
			writeOnlineBriefResponse(w)
		}
	}))
	defer provider.Close()

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previous)
	})
	service, ids := openSimulatedOverviewServiceWithModel(t, agentkit.ModelConfig{
		Mode:         agentkit.ModeOnline,
		BaseURL:      provider.URL,
		APIKey:       "test-key",
		Model:        "brief-fake",
		BriefTimeout: 20 * time.Millisecond,
	})
	overview, err := service.Overview(t.Context(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if overview.Brief.Mode != "deterministic_read_only" ||
		overview.Brief.Source != "rules" ||
		overview.Brief.FallbackReason != BriefFallbackTimeout {
		t.Fatalf("brief = %+v", overview.Brief)
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", calls.Load())
	}
	if output := logs.String(); !strings.Contains(output, "level=WARN") ||
		!strings.Contains(output, "reason=timeout") {
		t.Fatalf("fallback warning = %q", output)
	}
}

func TestKPIsReturnZeroRealizedImpactWithoutExecutedRuns(t *testing.T) {
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
		if metric.Availability != "available" || metric.Value == nil || *metric.Value != 0 {
			t.Fatalf("metric %q is not zero: %+v", key, metric)
		}
		if !strings.Contains(metric.Label, "已实现") ||
			!strings.Contains(metric.Formula, "仿真估算") {
			t.Fatalf("metric %q does not disclose its basis: %+v", key, metric)
		}
	}
	for _, key := range []string{"labor_saved_hours", "anomaly_closure_rate_pct"} {
		metric := metrics[key]
		if metric.Availability != "available" || metric.Value == nil {
			t.Fatalf("metric %q is unavailable: %+v", key, metric)
		}
	}
}

func TestKPIsRequireCompletedRunWithMatchingExecution(t *testing.T) {
	service, ids := openSimulatedOverviewService(t)
	items := newestAnomaliesWithImpact(t, service, 2)
	executedRunID := domain.RunID("run-realized-impact")
	unconfirmedRunID := domain.RunID("run-unconfirmed-impact")
	appendRunStarted(t, service, executedRunID, items[0].WaybillID)
	appendHumanApprovedExecution(
		t,
		service,
		executedRunID,
		domain.ApprovalID("approval-realized-impact"),
	)
	appendRunCompleted(t, service, executedRunID)
	appendRunStarted(t, service, unconfirmedRunID, items[1].WaybillID)
	if _, err := service.journal.Append(t.Context(), unconfirmedRunID, audit.Draft{
		EventID: "approval:unconfirmed-impact:executed",
		Actor:   audit.ActorSystem,
		Type:    audit.EventApprovalExecuted,
		Payload: map[string]any{"approval_id": "approval-unconfirmed-impact"},
	}); err != nil {
		t.Fatal(err)
	}
	appendRunCompleted(t, service, unconfirmedRunID)

	report, err := service.KPIs(context.Background(), ids, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	timeMetric, costMetric := simulationImpactMetrics(items[:1])
	assertRealizedImpact(t, report, *timeMetric.Value, *costMetric.Value)

	latestRunID := domain.RunID("run-latest-without-execution")
	appendRunStarted(t, service, latestRunID, items[0].WaybillID)
	appendRunCompleted(t, service, latestRunID)
	report, err = service.KPIs(context.Background(), ids, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	assertRealizedImpact(t, report, 0, 0)
}

func TestOverviewCachesBriefByAuthorizedScopeAndReturnsClones(t *testing.T) {
	generator := &briefGeneratorStub{result: generatedBriefFixture()}
	service, ids := openSimulatedOverviewServiceWithBrief(t, generator)

	var first Overview
	for index := 0; index < 10; index++ {
		overview, err := service.Overview(t.Context(), ids)
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			first = overview
		}
	}
	if generator.callCount() != 1 {
		t.Fatalf("generator calls = %d, want 1", generator.callCount())
	}

	first.Brief.Items[0].Headline = "mutated"
	again, err := service.Overview(t.Context(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if again.Brief.Items[0].Headline == "mutated" {
		t.Fatal("cached brief shared mutable item storage")
	}

	if _, err := service.Overview(t.Context(), ids[:5]); err != nil {
		t.Fatal(err)
	}
	if generator.callCount() != 2 {
		t.Fatalf("cross-scope generator calls = %d, want 2", generator.callCount())
	}
}

func TestOverviewCoalescesConcurrentBriefGeneration(t *testing.T) {
	generator := &blockingBriefGenerator{
		result:  generatedBriefFixture(),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	service, ids := openSimulatedOverviewServiceWithBrief(t, generator)

	const callers = 10
	errs := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := service.Overview(t.Context(), ids)
			errs <- err
		}()
	}
	<-generator.started
	close(generator.release)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if generator.callCount() != 1 {
		t.Fatalf("generator calls = %d, want 1", generator.callCount())
	}
}

func TestOverviewFallbackWarningIsSanitizedAndCached(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previous)
	})

	generator := &briefGeneratorStub{err: errors.New("provider-secret-detail")}
	service, ids := openSimulatedOverviewServiceWithBrief(t, generator)
	for range 2 {
		overview, err := service.Overview(t.Context(), ids)
		if err != nil {
			t.Fatal(err)
		}
		if overview.Brief.FallbackReason != BriefFallbackProvider {
			t.Fatalf("fallback reason = %q", overview.Brief.FallbackReason)
		}
	}
	if generator.callCount() != 1 {
		t.Fatalf("generator calls = %d, want 1", generator.callCount())
	}
	output := logs.String()
	if !strings.Contains(output, "reason=provider_error") ||
		strings.Contains(output, "provider-secret-detail") ||
		strings.Contains(output, string(ids[0])) {
		t.Fatalf("warning was not sanitized: %s", output)
	}
}

func TestSimulationImpactMetricsRequireACompletePopulation(t *testing.T) {
	complete := []platform.WaybillSummary{
		{
			Impact: &platform.SimulationImpact{
				NoActionETAHours:    12,
				PostActionETAHours:  8.5,
				AvoidedPenaltyCents: 120_000,
				ReassignDeltaCents:  20_000,
				HandlingCostCents:   5_000,
			},
		},
		{
			Impact: &platform.SimulationImpact{
				NoActionETAHours:    20,
				PostActionETAHours:  15,
				AvoidedPenaltyCents: 80_000,
				ReassignDeltaCents:  12_000,
				HandlingCostCents:   3_000,
			},
		},
	}
	timeMetric, costMetric := simulationImpactMetrics(complete)
	if timeMetric.Value == nil || *timeMetric.Value != 8.5 {
		t.Fatalf("time recovered = %+v", timeMetric)
	}
	if costMetric.Value == nil || *costMetric.Value != 1600 {
		t.Fatalf("cost impact = %+v", costMetric)
	}

	complete = append(complete, platform.WaybillSummary{})
	timeMetric, costMetric = simulationImpactMetrics(complete)
	for _, metric := range []KPIMetric{timeMetric, costMetric} {
		if metric.Availability != "unavailable" || metric.Value != nil || metric.Reason == "" {
			t.Fatalf("incomplete population metric = %+v", metric)
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
	return openSimulatedOverviewServiceWithBrief(t, nil)
}

func openSimulatedOverviewServiceWithBrief(
	t *testing.T,
	briefGenerator agentkit.BriefGenerator,
) (*Service, []domain.WaybillID) {
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
		BriefGenerator:      briefGenerator,
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

func openSimulatedOverviewServiceWithModel(
	t *testing.T,
	model agentkit.ModelConfig,
) (*Service, []domain.WaybillID) {
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
		Model:               model,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("close service: %v", err)
		}
	})
	catalog, err := loaded.Reads.Catalog.ListWaybills(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]domain.WaybillID, 0, len(catalog))
	for _, item := range catalog {
		ids = append(ids, item.WaybillID)
	}
	return service, ids
}

func writeOnlineBriefResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":     "response-brief",
		"model":  "brief-fake",
		"object": "response",
		"status": "completed",
		"output": []any{map[string]any{
			"id":   "message-brief",
			"type": "message",
			"role": "assistant",
			"content": []any{map[string]any{
				"type": "output_text",
				"text": `{"items":[` +
					`{"headline":"治理高频异常","body":"建议复盘共性","evidence_ids":["anomaly_mix"]},` +
					`{"headline":"聚焦线路风险","body":"建议前置预案","evidence_ids":["route_hotspot"]},` +
					`{"headline":"调配节点资源","body":"建议平衡负荷","evidence_ids":["hub_pressure"]}` +
					`]}`,
				"annotations": []any{},
			}},
		}},
		"usage": map[string]any{
			"input_tokens":  40,
			"output_tokens": 10,
			"total_tokens":  50,
		},
	})
}

type briefGeneratorStub struct {
	result agentkit.GeneratedBrief
	err    error
	calls  int
	input  agentkit.BriefInput
	mu     sync.Mutex
}

func (s *briefGeneratorStub) Generate(
	_ context.Context,
	input agentkit.BriefInput,
) (agentkit.GeneratedBrief, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.input = input
	return s.result, s.err
}

func (s *briefGeneratorStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type blockingBriefGenerator struct {
	result  agentkit.GeneratedBrief
	started chan struct{}
	release chan struct{}
	calls   int
	once    sync.Once
	mu      sync.Mutex
}

func (s *blockingBriefGenerator) Generate(
	ctx context.Context,
	_ agentkit.BriefInput,
) (agentkit.GeneratedBrief, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	s.once.Do(func() {
		close(s.started)
	})
	select {
	case <-ctx.Done():
		return agentkit.GeneratedBrief{}, ctx.Err()
	case <-s.release:
		return s.result, nil
	}
}

func (s *blockingBriefGenerator) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func generatedBriefFixture() agentkit.GeneratedBrief {
	return agentkit.GeneratedBrief{Items: []agentkit.GeneratedBriefItem{
		{
			Headline:    "治理高频异常",
			Body:        "建议复盘共性",
			EvidenceIDs: []string{agentkit.EvidenceAnomalyMix},
		},
		{
			Headline:    "聚焦线路风险",
			Body:        "建议前置运力",
			EvidenceIDs: []string{agentkit.EvidenceRouteHotspot},
		},
		{
			Headline:    "平衡节点资源",
			Body:        "建议调整排班",
			EvidenceIDs: []string{agentkit.EvidenceHubPressure},
		},
	}}
}

func newestAnomaliesWithImpact(
	t *testing.T,
	service *Service,
	count int,
) []platform.WaybillSummary {
	t.Helper()
	catalog, err := service.reads.Catalog.ListWaybills(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result := make([]platform.WaybillSummary, 0, count)
	for _, item := range catalog {
		if item.HasAnomaly && item.Impact != nil {
			result = append(result, item)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].LastRecordedAt.After(result[j].LastRecordedAt)
	})
	if len(result) < count {
		t.Fatalf("fixture has %d anomalous waybills with impact, want %d", len(result), count)
	}
	return result[:count]
}

func appendRunStarted(
	t *testing.T,
	service *Service,
	runID domain.RunID,
	waybillID domain.WaybillID,
) {
	t.Helper()
	if _, err := service.journal.Append(t.Context(), runID, audit.Draft{
		EventID: "run:" + string(runID) + ":started",
		Actor:   audit.ActorAgent,
		Type:    audit.EventRunStarted,
		Payload: runStartedPayload{
			IncidentID: domain.IncidentID("incident-" + string(runID)),
			WaybillID:  waybillID,
			Status:     domain.RunStarted,
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func appendHumanApprovedExecution(
	t *testing.T,
	service *Service,
	runID domain.RunID,
	approvalID domain.ApprovalID,
) {
	t.Helper()
	if _, err := service.journal.Append(t.Context(), runID, audit.Draft{
		EventID: "approval:" + string(approvalID) + ":confirmed",
		Actor:   audit.ActorHuman,
		Type:    audit.EventApprovalDecided,
		Payload: map[string]any{
			"approval_id": approvalID,
			"status":      approval.StatusConfirmed,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.journal.Append(t.Context(), runID, audit.Draft{
		EventID: "approval:" + string(approvalID) + ":executed",
		Actor:   audit.ActorSystem,
		Type:    audit.EventApprovalExecuted,
		Payload: map[string]any{"approval_id": approvalID},
	}); err != nil {
		t.Fatal(err)
	}
}

func appendRunCompleted(
	t *testing.T,
	service *Service,
	runID domain.RunID,
) {
	t.Helper()
	if _, err := service.journal.Append(t.Context(), runID, audit.Draft{
		EventID: "run:" + string(runID) + ":completed",
		Actor:   audit.ActorAgent,
		Type:    audit.EventRunCompleted,
		Payload: map[string]any{"status": domain.RunCompleted},
	}); err != nil {
		t.Fatal(err)
	}
}

func assertRealizedImpact(t *testing.T, report KPIReport, wantTime, wantCost float64) {
	t.Helper()
	metrics := make(map[string]KPIMetric, len(report.Metrics))
	for _, metric := range report.Metrics {
		metrics[metric.Key] = metric
	}
	for key, want := range map[string]float64{
		"time_recovered_hours": wantTime,
		"cost_impact_cny":      wantCost,
	} {
		metric := metrics[key]
		if metric.Value == nil || *metric.Value != want {
			t.Fatalf("metric %q = %+v, want %v", key, metric, want)
		}
	}
}
