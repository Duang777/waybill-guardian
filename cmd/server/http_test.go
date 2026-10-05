package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	agentkit "github.com/Duang777/waybill-guardian/internal/agent"
	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/guardian"
	"github.com/Duang777/waybill-guardian/internal/httpauth"
	"github.com/Duang777/waybill-guardian/internal/platform/tmssandbox"
	"github.com/Duang777/waybill-guardian/internal/storage"
	"github.com/Duang777/waybill-guardian/internal/tools"
)

func TestHTTPDemoFlowAndSSECursor(t *testing.T) {
	clients, _, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	service, err := guardian.Open(guardian.Config{
		DataDir:   t.TempDir(),
		Reads:     clients,
		StepDelay: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	server := httptest.NewServer(newHandler(service, newLocalAccess(t)))
	defer server.Close()

	response, err := http.Post(
		server.URL+"/api/demo/trigger",
		"application/json",
		strings.NewReader(`{}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("trigger status = %d", response.StatusCode)
	}
	var run guardian.RunView
	if err := json.NewDecoder(response.Body).Decode(&run); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	pending := waitForHTTPApproval(t, service, run.RunID)

	waybillResponse, err := http.Get(server.URL + "/api/waybills/" + string(run.WaybillID))
	if err != nil {
		t.Fatal(err)
	}
	if waybillResponse.StatusCode != http.StatusOK {
		t.Fatalf("waybill status = %d", waybillResponse.StatusCode)
	}
	var waybill guardian.WaybillView
	if err := json.NewDecoder(waybillResponse.Body).Decode(&waybill); err != nil {
		t.Fatal(err)
	}
	_ = waybillResponse.Body.Close()
	if waybill.Waybill.ShipperPhone != "138****1234" {
		t.Fatalf("unmasked shipper phone %q", waybill.Waybill.ShipperPhone)
	}

	confirmRequest, err := http.NewRequest(
		http.MethodPost,
		server.URL+"/api/approvals/"+string(pending.ID)+"/confirm",
		strings.NewReader(`{}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	confirmRequest.Header.Set("X-Actor", "forged-reviewer")
	confirmRequest.Header.Set("Content-Type", "application/json")
	confirmResponse, err := http.DefaultClient.Do(confirmRequest)
	if err != nil {
		t.Fatal(err)
	}
	if confirmResponse.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(confirmResponse.Body)
		t.Fatalf("confirm status = %d body=%s", confirmResponse.StatusCode, body)
	}
	var confirmed approval.Approval
	if err := json.NewDecoder(confirmResponse.Body).Decode(&confirmed); err != nil {
		t.Fatal(err)
	}
	_ = confirmResponse.Body.Close()
	if confirmed.Status != approval.StatusExecuted {
		t.Fatalf("approval status = %q", confirmed.Status)
	}
	if confirmed.DecidedBy != "local-demo-reviewer" {
		t.Fatalf("decided_by = %q, want local-demo-reviewer", confirmed.DecidedBy)
	}

	events, err := service.Replay(context.Background(), run.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 {
		t.Fatalf("events = %d, want at least 2", len(events))
	}
	cursor := events[len(events)-2].Seq
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		server.URL+"/api/runs/"+string(run.RunID)+"/timeline", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Last-Event-ID", strconv.FormatUint(uint64(cursor), 10))
	stream, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	scanner := bufio.NewScanner(stream.Body)
	var receivedID string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "id: ") {
			receivedID = strings.TrimPrefix(line, "id: ")
		}
		if strings.HasPrefix(line, "data: ") {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	cancel()
	if receivedID != strconv.FormatUint(uint64(cursor+1), 10) {
		t.Fatalf("resumed SSE id = %q, want %d", receivedID, cursor+1)
	}
}

func TestCrossOriginApprovalRequestsDoNotChangeState(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
	}{
		{
			name: "cross-site fetch metadata",
			headers: map[string]string{
				"Sec-Fetch-Site": "cross-site",
			},
		},
		{
			name:    "mismatched origin without fetch metadata",
			headers: map[string]string{"Origin": "https://evil.example"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, writes, server, pending := newPendingApprovalHTTPServer(t, "")

			response := postApprovalConfirmation(t, server.URL, pending.ID, test.headers)
			assertProblem(t, response, http.StatusForbidden, "cross_origin_denied")

			current, err := service.GetApproval(pending.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Status != approval.StatusPending {
				t.Fatalf("approval status = %q, want pending", current.Status)
			}
			if writes.WriteCount(domain.ActionReassign) != 0 {
				t.Fatal("cross-origin approval executed a platform write")
			}
		})
	}
}

func TestCrossOriginApprovalAllowsSupportedClients(t *testing.T) {
	tests := []struct {
		name           string
		allowedOrigins string
		headers        func(string) map[string]string
	}{
		{
			name: "non-browser client",
			headers: func(string) map[string]string {
				return nil
			},
		},
		{
			name: "same-origin browser",
			headers: func(serverURL string) map[string]string {
				return map[string]string{
					"Origin":         serverURL,
					"Sec-Fetch-Site": "same-origin",
				}
			},
		},
		{
			name:           "configured trusted origin",
			allowedOrigins: "https://console.example",
			headers: func(string) map[string]string {
				return map[string]string{
					"Origin":         "https://console.example",
					"Sec-Fetch-Site": "cross-site",
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, server, pending := newPendingApprovalHTTPServer(t, test.allowedOrigins)

			response := postApprovalConfirmation(
				t,
				server.URL,
				pending.ID,
				test.headers(server.URL),
			)
			defer response.Body.Close()
			if response.StatusCode != http.StatusAccepted {
				body, _ := io.ReadAll(response.Body)
				t.Fatalf("status = %d, want 202; body=%s", response.StatusCode, body)
			}
			var confirmed approval.Approval
			if err := json.NewDecoder(response.Body).Decode(&confirmed); err != nil {
				t.Fatal(err)
			}
			if confirmed.Status != approval.StatusExecuted {
				t.Fatalf("approval status = %q, want executed", confirmed.Status)
			}
		})
	}
}

func TestMutationEndpointsRequireJSONContentType(t *testing.T) {
	service, _, server, pending := newPendingApprovalHTTPServer(t, "")
	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "demo trigger", path: "/api/demo/trigger", body: `{}`},
		{
			name: "approval confirmation",
			path: "/api/approvals/" + string(pending.ID) + "/confirm",
			body: `{}`,
		},
		{
			name: "approval rejection",
			path: "/api/approvals/" + string(pending.ID) + "/reject",
			body: `{"reason":"manual review"}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequest(
				http.MethodPost,
				server.URL+test.path,
				strings.NewReader(test.body),
			)
			if err != nil {
				t.Fatal(err)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			assertProblem(t, response, http.StatusUnsupportedMediaType, "invalid_content_type")
		})
	}

	current, err := service.GetApproval(pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != approval.StatusPending {
		t.Fatalf("approval status = %q, want pending", current.Status)
	}
}

func TestWaybillCatalogAndExplicitRunAPI(t *testing.T) {
	reads, writes, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	service, err := guardian.Open(guardian.Config{
		DataDir:      t.TempDir(),
		Reads:        reads,
		WriteRuntime: writes,
		StepDelay:    0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	server := httptest.NewServer(newHandler(service, newLocalAccess(t)))
	defer server.Close()

	response, err := http.Get(server.URL + "/api/waybills")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("catalog status = %d", response.StatusCode)
	}
	var catalog struct {
		Waybills []guardian.WaybillCatalogItem `json:"waybills"`
	}
	if err := json.NewDecoder(response.Body).Decode(&catalog); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if len(catalog.Waybills) != 1 ||
		catalog.Waybills[0].WaybillID != "YD2026101001" ||
		!catalog.Waybills[0].HasAnomaly {
		t.Fatalf("catalog = %+v", catalog.Waybills)
	}

	response, err = http.Post(
		server.URL+"/api/runs",
		"text/plain",
		strings.NewReader(`{"waybill_id":"YD2026101001"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	assertProblem(t, response, http.StatusUnsupportedMediaType, "invalid_content_type")

	response, err = http.Post(
		server.URL+"/api/runs",
		"application/json",
		strings.NewReader(`{"waybill_id":"YD2026101001"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("start status = %d body=%s", response.StatusCode, body)
	}
	var run guardian.RunView
	if err := json.NewDecoder(response.Body).Decode(&run); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if run.WaybillID != "YD2026101001" {
		t.Fatalf("run = %+v", run)
	}

	response, err = http.Post(
		server.URL+"/api/runs",
		"application/json",
		strings.NewReader(`{"waybill_id":"YD2026101099"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	assertProblem(t, response, http.StatusNotFound, "not_found")
}

func TestTimelineStopsWritingToSlowClientAfterDeadline(t *testing.T) {
	clients, _, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	service, err := guardian.Open(guardian.Config{
		DataDir:   t.TempDir(),
		Reads:     clients,
		StepDelay: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	run, err := service.StartRun(context.Background(), "YD2026101001")
	if err != nil {
		t.Fatal(err)
	}

	writer := newDeadlineBlockingWriter()
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"http://127.0.0.1/api/runs/"+string(run.RunID)+"/timeline",
		nil,
	)
	request.SetPathValue("id", string(run.RunID))
	access := newLocalAccess(t)
	request = authenticateRequest(t, access, request)
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&api{service: service, access: access}).timeline(writer, request)
	}()

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		cancel()
		writer.release()
		<-done
		t.Fatal("timeline handler remained blocked on a slow client")
	}
	cancel()
	writer.release()
	if !writer.deadlineWasSet() {
		t.Fatal("timeline handler did not set a write deadline")
	}
}

func TestTimelineDoesNotWriteBufferedEventsAfterRequestCancellation(t *testing.T) {
	clients, _, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	service, err := guardian.Open(guardian.Config{
		DataDir:   t.TempDir(),
		Reads:     clients,
		StepDelay: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	run, err := service.StartRun(context.Background(), "YD2026101001")
	if err != nil {
		t.Fatal(err)
	}
	access := newLocalAccess(t)
	handler := &api{service: service, access: access}

	for range 32 {
		ctx, cancel := context.WithCancel(context.Background())
		request := httptest.NewRequestWithContext(
			ctx,
			http.MethodGet,
			"http://127.0.0.1/api/runs/"+string(run.RunID)+"/timeline",
			nil,
		)
		request.SetPathValue("id", string(run.RunID))
		request = authenticateRequest(t, access, request)
		writer := &cancelOnFlushWriter{
			ResponseRecorder: httptest.NewRecorder(),
			cancel:           cancel,
		}

		handler.timeline(writer, request)

		if strings.Contains(writer.Body.String(), "event:") {
			t.Fatal("timeline wrote an event after request cancellation")
		}
	}
}

func TestSSEWriteDeadlineUsesEarlierRequestDeadline(t *testing.T) {
	requestDeadline := time.Now().Add(time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), requestDeadline)
	defer cancel()
	writer := newDeadlineBlockingWriter()
	defer writer.release()

	if err := setSSEWriteDeadline(ctx, http.NewResponseController(writer)); err != nil {
		t.Fatal(err)
	}
	if got := writer.writeDeadline(); !got.Equal(requestDeadline) {
		t.Fatalf("write deadline = %v, want %v", got, requestDeadline)
	}
}

func TestTimelineRejectsWhenSubscriptionLimitIsReached(t *testing.T) {
	clients, _, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	service, err := guardian.Open(guardian.Config{
		DataDir:   t.TempDir(),
		Reads:     clients,
		StepDelay: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	run, err := service.StartRun(context.Background(), "YD2026101001")
	if err != nil {
		t.Fatal(err)
	}
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	access := newLocalAccess(t)
	handler := &api{service: service, access: access, sseSlots: slots}
	request := httptest.NewRequest(
		http.MethodGet,
		"http://127.0.0.1/api/runs/"+string(run.RunID)+"/timeline",
		nil,
	)
	request.SetPathValue("id", string(run.RunID))
	request = authenticateRequest(t, access, request)
	response := httptest.NewRecorder()

	handler.timeline(response, request)

	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", response.Code)
	}
	var problem map[string]map[string]string
	if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if problem["error"]["code"] != "too_many_streams" {
		t.Fatalf("error code = %q, want too_many_streams", problem["error"]["code"])
	}
}

func TestRejectValidationAndUnknownFields(t *testing.T) {
	clients, _, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	service, err := guardian.Open(guardian.Config{DataDir: t.TempDir(), Reads: clients})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	server := httptest.NewServer(newHandler(service, newLocalAccess(t)))
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/approvals/missing/reject",
		bytes.NewBufferString(`{"reason":"no","unexpected":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}
}

func TestWaybillErrorContract(t *testing.T) {
	clients, _, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	service, err := guardian.Open(guardian.Config{DataDir: t.TempDir(), Reads: clients})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	server := httptest.NewServer(newHandler(service, newLocalAccess(t)))
	defer server.Close()

	tests := []struct {
		name       string
		id         string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "invalid identifier",
			id:         "not-valid",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_waybill_id",
		},
		{
			name:       "missing waybill",
			id:         "YD9999999999",
			wantStatus: http.StatusNotFound,
			wantCode:   "not_found",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := http.Get(server.URL + "/api/waybills/" + test.id)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != test.wantStatus {
				body, _ := io.ReadAll(response.Body)
				t.Fatalf("status = %d, want %d; body=%s", response.StatusCode, test.wantStatus, body)
			}
			var problem map[string]map[string]string
			if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
				t.Fatal(err)
			}
			if problem["error"]["code"] != test.wantCode {
				t.Fatalf("error code = %q, want %q", problem["error"]["code"], test.wantCode)
			}
		})
	}
}

func TestHTTPRecoveryQueries(t *testing.T) {
	clients, _, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	service, err := guardian.Open(guardian.Config{
		DataDir:   t.TempDir(),
		Reads:     clients,
		StepDelay: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	server := httptest.NewServer(newHandler(service, newLocalAccess(t)))
	defer server.Close()

	run, err := service.StartRun(context.Background(), "YD2026101001")
	if err != nil {
		t.Fatal(err)
	}
	pending := waitForHTTPApproval(t, service, run.RunID)

	var runs struct {
		Runs []guardian.RunSummary `json:"runs"`
	}
	getJSON(t, server.URL+"/api/runs?status=active", &runs)
	if len(runs.Runs) != 1 || runs.Runs[0].RunID != run.RunID {
		t.Fatalf("active runs = %+v", runs.Runs)
	}
	var approvals struct {
		Approvals []guardian.PendingApprovalSummary `json:"approvals"`
	}
	getJSON(t, server.URL+"/api/approvals?status=pending", &approvals)
	if len(approvals.Approvals) != 1 || approvals.Approvals[0].ID != pending.ID {
		t.Fatalf("pending approvals = %+v", approvals.Approvals)
	}
	var snapshot guardian.RunSnapshot
	getJSON(t, server.URL+"/api/runs/"+string(run.RunID), &snapshot)
	if len(snapshot.Events) == 0 ||
		snapshot.Run.LastSeq != snapshot.Events[len(snapshot.Events)-1].Seq {
		t.Fatalf("inconsistent snapshot = %+v", snapshot)
	}

	response, err := http.Get(server.URL + "/api/runs")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing filter status = %d, want 400", response.StatusCode)
	}
}

func TestHTTPRecoveryQueriesReturnEmptyArrays(t *testing.T) {
	clients, _, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	service, err := guardian.Open(guardian.Config{DataDir: t.TempDir(), Reads: clients})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	server := httptest.NewServer(newHandler(service, newLocalAccess(t)))
	defer server.Close()

	var runs struct {
		Runs []guardian.RunSummary `json:"runs"`
	}
	getJSON(t, server.URL+"/api/runs?status=active", &runs)
	if runs.Runs == nil || len(runs.Runs) != 0 {
		t.Fatalf("active runs = %#v, want empty array", runs.Runs)
	}
	var approvals struct {
		Approvals []guardian.PendingApprovalSummary `json:"approvals"`
	}
	getJSON(t, server.URL+"/api/approvals?status=pending", &approvals)
	if approvals.Approvals == nil || len(approvals.Approvals) != 0 {
		t.Fatalf("pending approvals = %#v, want empty array", approvals.Approvals)
	}
}

func TestParseLastEventID(t *testing.T) {
	if value, err := parseLastEventID(""); err != nil || value != 0 {
		t.Fatalf("empty cursor = %d, %v", value, err)
	}
	if _, err := parseLastEventID("-1"); err == nil {
		t.Fatal("negative cursor was accepted")
	}
}

func TestParseTimelineCursorPrefersLastEventID(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/timeline?after=4", nil)
	cursor, err := parseTimelineCursor(request)
	if err != nil || cursor != 4 {
		t.Fatalf("query cursor = %d, %v", cursor, err)
	}
	request.Header.Set("Last-Event-ID", "7")
	cursor, err = parseTimelineCursor(request)
	if err != nil || cursor != 7 {
		t.Fatalf("header cursor = %d, %v", cursor, err)
	}
	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1/timeline?after=1&after=2", nil)
	if _, err := parseTimelineCursor(request); err == nil {
		t.Fatal("duplicate query cursor was accepted")
	}
}

func TestOpenRealPlatformRuntime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/waybill-capabilities" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{
			"adapter_id":"tms-reassign-sandbox-v1",
			"contract_version":"v1",
			"environment":"sandbox",
			"capabilities":[{
				"action":"tms.reassign",
				"operation":"reassignments",
				"key_scope":"tenant+operation",
				"key_retention_seconds":604800,
				"lookup_consistency_window_seconds":1,
				"same_request_replays":true,
				"mismatched_request_rejects":true,
				"lookup_by_key":true
			}]
		}`)
	}))
	defer server.Close()
	setValidRealPlatformEnv(t)
	t.Setenv("TMS_SANDBOX_BASE_URL", server.URL)

	runtime, err := openPlatformRuntime(t.Context(), "real", "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.profileID != tmssandbox.ProfileID ||
		runtime.readSource != fixtureReadSource ||
		runtime.reads.TMS == nil ||
		runtime.reads.Weather == nil ||
		runtime.reads.Catalog == nil {
		t.Fatalf("real platform runtime = %+v", runtime)
	}
	if _, ok := runtime.writeRuntime.(*tmssandbox.Adapter); !ok {
		t.Fatalf("write runtime = %T, want TMS sandbox adapter", runtime.writeRuntime)
	}
	expected := []domain.Action{
		domain.ActionGetWaybill,
		domain.ActionGetTracking,
		domain.ActionGetDriver,
		domain.ActionGetRoadWeather,
		domain.ActionReassign,
	}
	if len(runtime.activeActions) != len(expected) {
		t.Fatalf("active actions = %v", runtime.activeActions)
	}
	for index, action := range expected {
		if runtime.activeActions[index] != action {
			t.Fatalf("active actions = %v", runtime.activeActions)
		}
	}
}

func TestRealPlatformConfigFromEnv(t *testing.T) {
	setValidRealPlatformEnv(t)
	t.Setenv("PLATFORM_REQUEST_TIMEOUT", "4s")
	t.Setenv("PLATFORM_STARTUP_TIMEOUT", "6s")
	t.Setenv("EFFECT_RECONCILE_HORIZON", "48h")
	t.Setenv("EFFECT_RECONCILE_POLL_INTERVAL", "750ms")
	t.Setenv("PLATFORM_MAX_LOOKUP_CONSISTENCY_WINDOW", "45s")

	config, err := realPlatformConfigFromEnv("tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if config.profileID != tmssandbox.ProfileID ||
		config.readSource != "fixture-v1" ||
		config.adapter.BaseURL != "https://sandbox.example.test" ||
		config.adapter.Token != "secret-token" ||
		config.adapter.Account != "tenant-a" ||
		config.adapter.RequestTimeout != 4*time.Second ||
		config.adapter.StartupTimeout != 6*time.Second ||
		config.adapter.ReconciliationHorizon != 48*time.Hour ||
		config.reconcilePoll != 750*time.Millisecond ||
		config.adapter.MaxConsistencyWindow != 45*time.Second ||
		config.adapter.Clock == nil {
		t.Fatalf("real platform config = %+v", config)
	}
}

func TestRealPlatformConfigRejectsIncompleteOrCrossTenantProfile(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		value    string
		tenantID httpauth.TenantID
	}{
		{
			name:     "missing profile",
			key:      "REAL_PLATFORM_PROFILE",
			tenantID: "tenant-a",
		},
		{
			name:     "implicit reads",
			key:      "REAL_READ_SOURCE",
			tenantID: "tenant-a",
		},
		{
			name:     "missing URL",
			key:      "TMS_SANDBOX_BASE_URL",
			tenantID: "tenant-a",
		},
		{
			name:     "missing token",
			key:      "TMS_SANDBOX_TOKEN",
			tenantID: "tenant-a",
		},
		{
			name:     "account mismatch",
			tenantID: "tenant-b",
		},
		{
			name:     "invalid timeout",
			key:      "PLATFORM_REQUEST_TIMEOUT",
			value:    "0s",
			tenantID: "tenant-a",
		},
		{
			name:     "invalid reconcile poll",
			key:      "EFFECT_RECONCILE_POLL_INTERVAL",
			value:    "0s",
			tenantID: "tenant-a",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setValidRealPlatformEnv(t)
			if test.key != "" {
				t.Setenv(test.key, test.value)
			}
			if _, err := realPlatformConfigFromEnv(test.tenantID); err == nil {
				t.Fatal("invalid real platform configuration was accepted")
			}
		})
	}
}

func TestRuntimeStorageConfiguration(t *testing.T) {
	if err := validateRuntimeModes("mock", "jsonl", httpauth.ModeLocal); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeModes("mock", "postgres", httpauth.ModeJWT); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeModes("file", "jsonl", httpauth.ModeLocal); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeModes("file", "postgres", httpauth.ModeLocal); err == nil {
		t.Fatal("file platform accepted PostgreSQL storage")
	}
	if err := validateRuntimeModes("file", "jsonl", httpauth.ModeJWT); err == nil {
		t.Fatal("file platform accepted JWT authentication")
	}
	if err := validateRuntimeModes("real", "jsonl", httpauth.ModeJWT); err == nil {
		t.Fatal("real platform accepted JSONL storage")
	}
	if err := validateRuntimeModes("real", "postgres", httpauth.ModeLocal); err == nil {
		t.Fatal("real platform accepted local authentication")
	}
	if err := validateRuntimeModes("unknown", "jsonl", httpauth.ModeLocal); err == nil {
		t.Fatal("unknown platform mode was accepted")
	}
}

func TestOpenFilePlatformRuntime(t *testing.T) {
	t.Setenv(
		"DATA_FILE",
		filepath.Join("..", "..", "data", "templates", "waybills-v1.csv"),
	)
	runtime, err := openPlatformRuntime(t.Context(), "file", "local-demo")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.profileID != tools.FixtureRuntimeAdapterID ||
		!strings.HasPrefix(runtime.readSource, "file:csv:v1:template-v1:") ||
		runtime.reads.TMS == nil ||
		runtime.reads.Weather == nil ||
		runtime.reads.Catalog == nil {
		t.Fatalf("file runtime = %+v", runtime)
	}
	catalog, err := runtime.reads.Catalog.ListWaybills(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 1 || catalog[0].WaybillID != "YD2026101001" {
		t.Fatalf("catalog = %+v", catalog)
	}
}

func TestOpenFilePlatformRuntimeRequiresDataFile(t *testing.T) {
	t.Setenv("DATA_FILE", "")
	if _, err := openPlatformRuntime(t.Context(), "file", "local-demo"); err == nil {
		t.Fatal("file platform accepted an empty DATA_FILE")
	}
}

func setValidRealPlatformEnv(t *testing.T) {
	t.Helper()
	for name, value := range map[string]string{
		"REAL_PLATFORM_PROFILE":                  tmssandbox.ProfileID,
		"REAL_READ_SOURCE":                       "fixture-v1",
		"TMS_SANDBOX_BASE_URL":                   "https://sandbox.example.test",
		"TMS_SANDBOX_TOKEN":                      "secret-token",
		"TMS_SANDBOX_ACCOUNT":                    "tenant-a",
		"PLATFORM_REQUEST_TIMEOUT":               "",
		"PLATFORM_STARTUP_TIMEOUT":               "",
		"EFFECT_RECONCILE_HORIZON":               "",
		"EFFECT_RECONCILE_POLL_INTERVAL":         "",
		"PLATFORM_MAX_LOOKUP_CONSISTENCY_WINDOW": "",
	} {
		t.Setenv(name, value)
	}
}

func TestEventConfigFromEnv(t *testing.T) {
	clearEventConfigEnv(t)
	config, err := eventConfigFromEnv(storage.ModePostgres)
	if err != nil {
		t.Fatal(err)
	}
	if config.outboxEnabled ||
		config.outboxBatchSize != 10 ||
		config.outboxWorkers != 4 ||
		config.outboxPoll != 250*time.Millisecond ||
		config.outboxLeaseTTL != 30*time.Second ||
		config.outboxStatsPoll != 15*time.Second ||
		config.outboxTimeout != 10*time.Second ||
		config.metricsAddr != "" {
		t.Fatalf("default event config = %+v", config)
	}

	t.Setenv("OUTBOX_ENABLED", "true")
	t.Setenv("OUTBOX_URL", "https://events.example.test/v1/events")
	t.Setenv("OUTBOX_TOKEN", "secret-token")
	t.Setenv("OUTBOX_BATCH_SIZE", "20")
	t.Setenv("OUTBOX_CONCURRENCY", "5")
	t.Setenv("OUTBOX_POLL_INTERVAL", "500ms")
	t.Setenv("OUTBOX_LEASE_TTL", "45s")
	t.Setenv("OUTBOX_STATS_INTERVAL", "20s")
	t.Setenv("OUTBOX_HTTP_TIMEOUT", "8s")
	t.Setenv("METRICS_ADDR", "127.0.0.1:9090")
	config, err = eventConfigFromEnv(storage.ModePostgres)
	if err != nil {
		t.Fatal(err)
	}
	if !config.outboxEnabled ||
		config.outboxURL != "https://events.example.test/v1/events" ||
		config.outboxToken != "secret-token" ||
		config.outboxBatchSize != 20 ||
		config.outboxWorkers != 5 ||
		config.outboxPoll != 500*time.Millisecond ||
		config.outboxLeaseTTL != 45*time.Second ||
		config.outboxStatsPoll != 20*time.Second ||
		config.outboxTimeout != 8*time.Second ||
		config.metricsAddr != "127.0.0.1:9090" {
		t.Fatalf("configured event config = %+v", config)
	}
}

func TestEventConfigFromEnvRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name        string
		storageMode storage.Mode
		key         string
		value       string
	}{
		{
			name:        "dispatcher with JSONL",
			storageMode: storage.ModeJSONL,
			key:         "OUTBOX_ENABLED",
			value:       "true",
		},
		{
			name:        "metrics with JSONL",
			storageMode: storage.ModeJSONL,
			key:         "METRICS_ADDR",
			value:       "127.0.0.1:9090",
		},
		{
			name:        "invalid enabled",
			storageMode: storage.ModePostgres,
			key:         "OUTBOX_ENABLED",
			value:       "sometimes",
		},
		{
			name:        "invalid batch",
			storageMode: storage.ModePostgres,
			key:         "OUTBOX_BATCH_SIZE",
			value:       "101",
		},
		{
			name:        "invalid workers",
			storageMode: storage.ModePostgres,
			key:         "OUTBOX_CONCURRENCY",
			value:       "0",
		},
		{
			name:        "invalid lease",
			storageMode: storage.ModePostgres,
			key:         "OUTBOX_LEASE_TTL",
			value:       "0s",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearEventConfigEnv(t)
			t.Setenv(test.key, test.value)
			if _, err := eventConfigFromEnv(test.storageMode); err == nil {
				t.Fatalf("%s=%q was accepted", test.key, test.value)
			}
		})
	}

	clearEventConfigEnv(t)
	t.Setenv("OUTBOX_ENABLED", "true")
	if _, err := eventConfigFromEnv(storage.ModePostgres); err == nil {
		t.Fatal("enabled dispatcher accepted missing URL and token")
	}
}

func TestRunComponentsCancelsAndDrainsPeers(t *testing.T) {
	failed := errors.New("listener failed")
	peerStopped := make(chan struct{})
	err := runComponents(t.Context(),
		func(context.Context) error {
			return failed
		},
		func(ctx context.Context) error {
			<-ctx.Done()
			close(peerStopped)
			return nil
		},
	)
	if !errors.Is(err, failed) {
		t.Fatalf("runComponents error = %v, want listener failure", err)
	}
	select {
	case <-peerStopped:
	default:
		t.Fatal("peer component was not drained")
	}
}

func clearEventConfigEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"OUTBOX_ENABLED",
		"OUTBOX_URL",
		"OUTBOX_TOKEN",
		"OUTBOX_BATCH_SIZE",
		"OUTBOX_CONCURRENCY",
		"OUTBOX_POLL_INTERVAL",
		"OUTBOX_LEASE_TTL",
		"OUTBOX_STATS_INTERVAL",
		"OUTBOX_HTTP_TIMEOUT",
		"METRICS_ADDR",
	} {
		t.Setenv(name, "")
	}
}

func TestPostgresConfigFromEnv(t *testing.T) {
	t.Setenv("DATABASE_URL", " postgres://localhost/waybill ")
	t.Setenv("PG_MAX_CONNS", "12")
	t.Setenv("PG_MIN_CONNS", "2")
	t.Setenv("PG_STARTUP_TIMEOUT", "5s")

	config, err := postgresConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.DatabaseURL != "postgres://localhost/waybill" ||
		config.MaxConns != 12 ||
		config.MinConns != 2 ||
		config.StartupTimeout != 5*time.Second {
		t.Fatalf("PostgreSQL config = %+v", config)
	}
}

func TestPostgresConfigFromEnvRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "maximum connections", key: "PG_MAX_CONNS", value: "many"},
		{name: "minimum connections", key: "PG_MIN_CONNS", value: "0.5"},
		{name: "startup duration", key: "PG_STARTUP_TIMEOUT", value: "soon"},
		{name: "zero startup duration", key: "PG_STARTUP_TIMEOUT", value: "0s"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("PG_MAX_CONNS", "")
			t.Setenv("PG_MIN_CONNS", "")
			t.Setenv("PG_STARTUP_TIMEOUT", "")
			t.Setenv(test.key, test.value)
			if _, err := postgresConfigFromEnv(); err == nil {
				t.Fatalf("%s=%q was accepted", test.key, test.value)
			}
		})
	}
}

func TestStrictDurationEnv(t *testing.T) {
	t.Setenv("HISTORY_RETENTION", "48h")
	got, err := strictDurationEnv("HISTORY_RETENTION", 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got != 48*time.Hour {
		t.Fatalf("history retention = %s, want 48h", got)
	}

	for _, value := range []string{"forever", "0s", "-1h"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("HISTORY_RETENTION", value)
			if _, err := strictDurationEnv("HISTORY_RETENTION", 7*24*time.Hour); err == nil {
				t.Fatalf("HISTORY_RETENTION=%q was accepted", value)
			}
		})
	}
}

func TestModelConfigFromEnv(t *testing.T) {
	t.Setenv("AGENT_MODE", "online")
	t.Setenv("LLM_API_STYLE", "chat_completions")
	t.Setenv("LLM_BASE_URL", " https://example.com/v1 ")
	t.Setenv("LLM_API_KEY", " secret ")
	t.Setenv("LLM_MODEL", " model-1 ")
	t.Setenv("LLM_REQUEST_TIMEOUT", "750ms")
	t.Setenv("BRIEF_TIMEOUT", "8s")
	t.Setenv("LLM_MAX_OUTPUT_TOKENS", "2048")

	config, err := modelConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode != "online" ||
		config.APIStyle != "chat_completions" ||
		config.BaseURL != "https://example.com/v1" ||
		config.APIKey != "secret" ||
		config.Model != "model-1" ||
		config.RequestTimeout != 750*time.Millisecond ||
		config.BriefTimeout != 8*time.Second ||
		config.MaxOutputTokens != 2048 {
		t.Fatalf("model config = %+v", config)
	}
}

func TestModelConfigFromEnvRejectsInvalidLimits(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "timeout", key: "LLM_REQUEST_TIMEOUT", value: "0s"},
		{name: "brief timeout", key: "BRIEF_TIMEOUT", value: "0s"},
		{name: "tokens", key: "LLM_MAX_OUTPUT_TOKENS", value: "32769"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("AGENT_MODE", "online")
			t.Setenv("LLM_REQUEST_TIMEOUT", "")
			t.Setenv("BRIEF_TIMEOUT", "")
			t.Setenv("LLM_MAX_OUTPUT_TOKENS", "")
			t.Setenv(test.key, test.value)
			if _, err := modelConfigFromEnv(); err == nil {
				t.Fatalf("%s=%q was accepted", test.key, test.value)
			}
		})
	}
}

func TestModelConfigFromEnvIgnoresOnlineLimitsInOfflineMode(t *testing.T) {
	t.Setenv("AGENT_MODE", "offline")
	t.Setenv("LLM_REQUEST_TIMEOUT", "invalid")
	t.Setenv("BRIEF_TIMEOUT", "invalid")
	t.Setenv("LLM_MAX_OUTPUT_TOKENS", "invalid")

	config, err := modelConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode != agentkit.ModeOffline ||
		config.RequestTimeout != 0 ||
		config.BriefTimeout != 0 ||
		config.MaxOutputTokens != 0 {
		t.Fatalf("offline model config = %+v", config)
	}
}

func TestCheckpointKeyFromEnv(t *testing.T) {
	t.Setenv(
		"CHECKPOINT_ENCRYPTION_KEY",
		"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	)
	key, err := checkpointKeyFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if string(key) != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("checkpoint key = %q", key)
	}
}

func TestCheckpointKeyFromEnvRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"", "not-base64", "c2hvcnQ="} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("CHECKPOINT_ENCRYPTION_KEY", value)
			if _, err := checkpointKeyFromEnv(); err == nil {
				t.Fatalf("CHECKPOINT_ENCRYPTION_KEY=%q was accepted", value)
			}
		})
	}
}

func TestValidateHTTPAddrAllowsOnlyExplicitLoopback(t *testing.T) {
	allowed := []string{
		defaultHTTPAddr,
		"127.0.0.2:9000",
		"[::1]:8080",
	}
	for _, addr := range allowed {
		if err := validateHTTPAddr(addr, httpauth.ModeLocal, false); err != nil {
			t.Errorf("validateHTTPAddr(%q) = %v", addr, err)
		}
	}

	rejected := []string{
		":8080",
		"0.0.0.0:8080",
		"[::]:8080",
		"192.168.1.10:8080",
		"localhost:8080",
		"example.com:8080",
		"127.0.0.1",
	}
	for _, addr := range rejected {
		if err := validateHTTPAddr(addr, httpauth.ModeLocal, false); err == nil {
			t.Errorf("validateHTTPAddr(%q) unexpectedly succeeded", addr)
		}
	}
	if err := validateHTTPAddr("0.0.0.0:8080", httpauth.ModeJWT, false); err != nil {
		t.Fatalf("JWT listener was rejected: %v", err)
	}
	if err := validateHTTPAddr("0.0.0.0:8080", httpauth.ModeLocal, true); err != nil {
		t.Fatalf("explicit local container listener was rejected: %v", err)
	}
	if err := validateHTTPAddr("not-an-address", httpauth.ModeLocal, true); err == nil {
		t.Fatal("invalid listener was accepted with ALLOW_NON_LOOPBACK_LOCAL")
	}
}

func TestCrossOriginProtectionRejectsInvalidConfiguration(t *testing.T) {
	for _, value := range []string{
		"https://console.example,",
		"console.example",
		"https://console.example/path",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := crossOriginProtection(value); err == nil {
				t.Fatalf("ALLOWED_ORIGINS=%q was accepted", value)
			}
		})
	}
}

func TestHandlerRejectsNonLoopbackHost(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://attacker.example/healthz", nil)
	response := httptest.NewRecorder()

	newHandler(nil, newLocalAccess(t)).ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.Code)
	}
	var problem map[string]map[string]string
	if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if problem["error"]["code"] != "invalid_host" {
		t.Fatalf("error code = %q, want invalid_host", problem["error"]["code"])
	}
}

func TestHandlerAcceptsLoopbackHostWithoutPort(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/healthz", nil)
	response := httptest.NewRecorder()

	newHandler(nil, newLocalAccess(t)).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
}

func TestHandlerServesConfiguredFrontendWithoutShadowingHealth(t *testing.T) {
	staticDir := t.TempDir()
	const index = "<!doctype html><title>waybill frontend</title>"
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}
	frontend, err := staticFileHandler(staticDir)
	if err != nil {
		t.Fatal(err)
	}
	handler := newHandlerWithFrontend(
		nil,
		newLocalAccess(t),
		nil,
		nil,
		frontend,
		nil,
		defaultCrossOriginProtection(),
	)

	pageRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	pageResponse := httptest.NewRecorder()
	handler.ServeHTTP(pageResponse, pageRequest)
	if pageResponse.Code != http.StatusOK {
		t.Fatalf("frontend status = %d, want 200", pageResponse.Code)
	}
	if pageResponse.Body.String() != index {
		t.Fatalf("frontend body = %q, want %q", pageResponse.Body.String(), index)
	}

	healthRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/healthz", nil)
	healthResponse := httptest.NewRecorder()
	handler.ServeHTTP(healthResponse, healthRequest)
	if healthResponse.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", healthResponse.Code)
	}
	if strings.Contains(healthResponse.Body.String(), "waybill frontend") {
		t.Fatal("frontend handler shadowed /healthz")
	}

	for _, path := range []string{"/", "/healthz"} {
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s status = %d, want 405", path, response.Code)
		}
	}
}

func TestStaticFileHandlerRequiresIndex(t *testing.T) {
	handler, err := staticFileHandler("")
	if err != nil {
		t.Fatal(err)
	}
	if handler != nil {
		t.Fatal("empty WEB_STATIC_DIR created a handler")
	}
	if _, err := staticFileHandler(t.TempDir()); err == nil {
		t.Fatal("WEB_STATIC_DIR without index.html was accepted")
	}
}

func TestHandlerChecksRemoteAddressForContainerLocalMode(t *testing.T) {
	trusted := netip.MustParseAddr("192.0.2.10")
	handler := newHandlerWithFrontend(
		nil,
		newLocalAccess(t),
		nil,
		nil,
		nil,
		[]netip.Addr{trusted},
		defaultCrossOriginProtection(),
	)

	untrustedRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/healthz", nil)
	untrustedRequest.RemoteAddr = "192.0.2.11:41000"
	untrustedResponse := httptest.NewRecorder()
	handler.ServeHTTP(untrustedResponse, untrustedRequest)
	if untrustedResponse.Code != http.StatusForbidden {
		t.Fatalf("untrusted status = %d, want 403", untrustedResponse.Code)
	}

	trustedRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/healthz", nil)
	trustedRequest.RemoteAddr = "192.0.2.10:41000"
	trustedResponse := httptest.NewRecorder()
	handler.ServeHTTP(trustedResponse, trustedRequest)
	if trustedResponse.Code != http.StatusOK {
		t.Fatalf("trusted status = %d, want 200", trustedResponse.Code)
	}
}

func TestLocalTrustedRemotesRequiresSpecificAddress(t *testing.T) {
	t.Setenv("LOCAL_TRUSTED_REMOTE", "")
	if _, err := localTrustedRemotes("0.0.0.0:8080", httpauth.ModeLocal, true); err == nil {
		t.Fatal("non-loopback local listener without trusted remote was accepted")
	}

	t.Setenv("LOCAL_TRUSTED_REMOTE", "0.0.0.0")
	if _, err := localTrustedRemotes("0.0.0.0:8080", httpauth.ModeLocal, true); err == nil {
		t.Fatal("unspecified trusted remote was accepted")
	}

	t.Setenv("LOCAL_TRUSTED_REMOTE", "192.0.2.10")
	addresses, err := localTrustedRemotes("0.0.0.0:8080", httpauth.ModeLocal, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 1 || addresses[0] != netip.MustParseAddr("192.0.2.10") {
		t.Fatalf("trusted addresses = %v", addresses)
	}
}

func TestParseDefaultIPv4Gateway(t *testing.T) {
	const routes = `Iface	Destination	Gateway	Flags	RefCnt	Use	Metric	Mask	MTU	Window	IRTT
eth0	00000000	01E4A8C0	0003	0	0	0	00000000	0	0	0
eth0	00E4A8C0	00000000	0001	0	0	0	00FFFFFF	0	0	0
`
	address, err := parseDefaultIPv4Gateway(routes)
	if err != nil {
		t.Fatal(err)
	}
	if want := netip.MustParseAddr("192.168.228.1"); address != want {
		t.Fatalf("gateway = %s, want %s", address, want)
	}
}

func TestServeWaitsForActiveHandlerDuringShutdown(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	server := &http.Server{
		Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			close(started)
			<-release
			close(finished)
		}),
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- serve(ctx, server, listener)
	}()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		response, requestErr := http.Get("http://" + listener.Addr().String())
		if requestErr == nil {
			_ = response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}

	cancel()
	select {
	case err := <-serveDone:
		t.Fatalf("serve returned before active handler finished: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("handler did not finish")
	}
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
	<-requestDone
}

func TestServeCancelsLongLivedHandlerDuringShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	stopped := make(chan struct{})
	server := &http.Server{
		Handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			close(started)
			<-r.Context().Done()
			close(stopped)
		}),
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- serve(ctx, server, listener)
	}()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		response, requestErr := http.Get("http://" + listener.Addr().String())
		if requestErr == nil {
			_ = response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}

	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("request context was not canceled")
	}
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
	<-requestDone
}

type deadlineBlockingWriter struct {
	header http.Header

	mu          sync.Mutex
	deadlineSet bool
	deadline    time.Time
	timeout     chan struct{}
	unblock     chan struct{}
	timeoutOnce sync.Once
	releaseOnce sync.Once
}

func newDeadlineBlockingWriter() *deadlineBlockingWriter {
	return &deadlineBlockingWriter{
		header:  make(http.Header),
		timeout: make(chan struct{}),
		unblock: make(chan struct{}),
	}
}

func (w *deadlineBlockingWriter) Header() http.Header {
	return w.header
}

func (*deadlineBlockingWriter) WriteHeader(int) {}

func (w *deadlineBlockingWriter) Write(payload []byte) (int, error) {
	select {
	case <-w.timeout:
		return 0, context.DeadlineExceeded
	case <-w.unblock:
		return len(payload), nil
	}
}

func (*deadlineBlockingWriter) Flush() {}

func (w *deadlineBlockingWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	w.deadlineSet = true
	w.deadline = deadline
	w.mu.Unlock()
	w.timeoutOnce.Do(func() {
		time.AfterFunc(20*time.Millisecond, func() {
			close(w.timeout)
		})
	})
	return nil
}

func (w *deadlineBlockingWriter) deadlineWasSet() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.deadlineSet
}

func (w *deadlineBlockingWriter) writeDeadline() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.deadline
}

func (w *deadlineBlockingWriter) release() {
	w.releaseOnce.Do(func() {
		close(w.unblock)
	})
}

type cancelOnFlushWriter struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
	once   sync.Once
}

func (w *cancelOnFlushWriter) Flush() {
	w.once.Do(w.cancel)
}

func (*cancelOnFlushWriter) SetWriteDeadline(time.Time) error {
	return nil
}

func newPendingApprovalHTTPServer(
	t *testing.T,
	allowedOrigins string,
) (*guardian.Service, *tools.FixtureWriteRuntime, *httptest.Server, approval.Approval) {
	t.Helper()
	service, writes := newHTTPAuthService(t)
	protection, err := crossOriginProtection(allowedOrigins)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newHandlerWithFrontend(
		service,
		newLocalAccess(t),
		nil,
		nil,
		nil,
		nil,
		protection,
	))
	t.Cleanup(server.Close)
	run, err := service.StartRun(context.Background(), "YD2026101001")
	if err != nil {
		t.Fatal(err)
	}
	return service, writes, server, waitForHTTPApproval(t, service, run.RunID)
}

func postApprovalConfirmation(
	t *testing.T,
	serverURL string,
	id domain.ApprovalID,
	headers map[string]string,
) *http.Response {
	t.Helper()
	request, err := http.NewRequest(
		http.MethodPost,
		serverURL+"/api/approvals/"+string(id)+"/confirm",
		strings.NewReader(`{}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func waitForHTTPApproval(
	t *testing.T,
	service *guardian.Service,
	runID domain.RunID,
) approval.Approval {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		value, err := service.CurrentApproval(runID)
		if err == nil {
			return value
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for approval")
	return approval.Approval{}
}

func getJSON(t *testing.T, url string, target any) {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("GET %s status = %d body=%s", url, response.StatusCode, body)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func newLocalAccess(t *testing.T) *httpauth.Boundary {
	t.Helper()
	access, err := httpauth.New(httpauth.Config{
		Mode:     httpauth.ModeLocal,
		TenantID: "local-demo",
	})
	if err != nil {
		t.Fatal(err)
	}
	return access
}

func authenticateRequest(
	t *testing.T,
	access *httpauth.Boundary,
	request *http.Request,
) *http.Request {
	t.Helper()
	authenticated, err := access.Authenticate(request)
	if err != nil {
		t.Fatal(err)
	}
	return authenticated
}
