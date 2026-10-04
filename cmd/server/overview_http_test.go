package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/guardian"
	"github.com/Duang777/waybill-guardian/internal/platform/filestore"
)

func TestOverviewKPIAndBatchHTTP(t *testing.T) {
	service := newSimulatedHTTPService(t, 8)
	server := httptest.NewServer(newHandler(service, newLocalAccess(t)))
	defer server.Close()

	var overview guardian.Overview
	getJSON(t, server.URL+"/api/overview", &overview)
	if overview.Totals.Waybills != 200 ||
		overview.Totals.Anomalies != 67 ||
		len(overview.Brief.Items) != 3 {
		t.Fatalf("overview = %+v", overview.Totals)
	}

	var report guardian.KPIReport
	getJSON(t, server.URL+"/api/kpis?window=24h", &report)
	if len(report.Metrics) < 4 || report.Window != "24h0m0s" {
		t.Fatalf("KPI report = %+v", report)
	}

	ids := make([]domain.WaybillID, 0, 5)
	for _, item := range overview.Anomalies[:5] {
		ids = append(ids, item.WaybillID)
	}
	body, err := json.Marshal(map[string]any{"waybill_ids": ids})
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(
		server.URL+"/api/runs:batch",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("batch status = %d body=%s", response.StatusCode, raw)
	}
	var result guardian.BatchRunResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Requested != 5 || result.Accepted != 5 || len(result.Results) != 5 {
		t.Fatalf("batch result = %+v", result)
	}
	runIDs := make(map[domain.RunID]struct{}, 5)
	for _, item := range result.Results {
		if item.Run == nil || item.Run.WaybillID != item.WaybillID {
			t.Fatalf("batch item = %+v", item)
		}
		runIDs[item.Run.RunID] = struct{}{}
	}
	if len(runIDs) != 5 {
		t.Fatalf("unique run IDs = %d, want 5", len(runIDs))
	}
}

func TestBatchHTTPValidatesShapeBeforeStartingRuns(t *testing.T) {
	service := newSimulatedHTTPService(t, 8)
	server := httptest.NewServer(newHandler(service, newLocalAccess(t)))
	defer server.Close()

	tests := []string{
		`{}`,
		`{"waybill_ids":[]}`,
		`{"waybill_ids":["YD2026100001"],"filter":{"limit":5}}`,
		`{"waybill_ids":["YD2026100001","YD2026100001"]}`,
		`{"filter":{"limit":0}}`,
	}
	for _, body := range tests {
		response, err := http.Post(
			server.URL+"/api/runs:batch",
			"application/json",
			bytes.NewBufferString(body),
		)
		if err != nil {
			t.Fatal(err)
		}
		assertProblem(t, response, http.StatusBadRequest, "invalid_batch")
	}
}

func TestBatchHTTPReturnsPerItemCapacityResults(t *testing.T) {
	service := newSimulatedHTTPService(t, 1)
	server := httptest.NewServer(newHandler(service, newLocalAccess(t)))
	defer server.Close()

	response, err := http.Post(
		server.URL+"/api/runs:batch",
		"application/json",
		bytes.NewBufferString(
			`{"waybill_ids":["YD2026100001","YD2026100004"]}`,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("batch status = %d body=%s", response.StatusCode, raw)
	}
	var result guardian.BatchRunResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Accepted != 1 ||
		result.Results[0].Run == nil ||
		result.Results[1].Error == nil ||
		result.Results[1].Error.Code != "run_capacity_reached" {
		t.Fatalf("capacity result = %+v", result)
	}
}

func TestOverviewHTTPFiltersBeforeAggregation(t *testing.T) {
	service := newSimulatedHTTPService(t, 8)
	access, privateKey := newJWTAccess(t)
	server := httptest.NewServer(newHandler(service, access))
	defer server.Close()
	token := authToken(
		t,
		privateKey,
		"tenant-a",
		[]string{"viewer"},
		false,
		[]string{"YD2026100001"},
	)

	response := doAuthenticatedRequest(
		t,
		http.MethodGet,
		server.URL+"/api/overview",
		token,
		nil,
	)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("overview status = %d body=%s", response.StatusCode, raw)
	}
	var overview guardian.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	if overview.Totals.Waybills != 1 ||
		len(overview.Anomalies) != 1 ||
		overview.Anomalies[0].WaybillID != "YD2026100001" {
		t.Fatalf("scoped overview leaked data: %+v", overview.Totals)
	}
}

func TestBatchHTTPAuthorizesAllExplicitIDsBeforeStarting(t *testing.T) {
	service := newSimulatedHTTPService(t, 8)
	access, privateKey := newJWTAccess(t)
	server := httptest.NewServer(newHandler(service, access))
	defer server.Close()
	token := authToken(
		t,
		privateKey,
		"tenant-a",
		[]string{"dispatcher"},
		false,
		[]string{"YD2026100001"},
	)

	response := doAuthenticatedRequest(
		t,
		http.MethodPost,
		server.URL+"/api/runs:batch",
		token,
		bytes.NewBufferString(
			`{"waybill_ids":["YD2026100001","YD2026100004"]}`,
		),
	)
	assertProblem(t, response, http.StatusForbidden, "forbidden")
	runs, err := service.ListActiveRuns(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("unauthorized batch started %d runs", len(runs))
	}
}

func TestKPIWindowValidation(t *testing.T) {
	service := newSimulatedHTTPService(t, 8)
	server := httptest.NewServer(newHandler(service, newLocalAccess(t)))
	defer server.Close()

	for _, query := range []string{"", "?window=0h", "?window=30m", "?window=169h", "?window=24h&x=1"} {
		response, err := http.Get(server.URL + "/api/kpis" + query)
		if err != nil {
			t.Fatal(err)
		}
		assertProblem(t, response, http.StatusBadRequest, "invalid_window")
	}
}

func newSimulatedHTTPService(t *testing.T, maxConcurrentRuns int) *guardian.Service {
	t.Helper()
	loaded, err := filestore.Load(
		filepath.Join("..", "..", "data", "simulated", "waybills-v1.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := guardian.Open(guardian.Config{
		DataDir:           t.TempDir(),
		Reads:             loaded.Reads,
		ReadSource:        loaded.Source.String(),
		StepDelay:         time.Second,
		MaxConcurrentRuns: maxConcurrentRuns,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("close service: %v", err)
		}
	})
	return service
}
