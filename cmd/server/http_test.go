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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/guardian"
	"github.com/Duang777/waybill-guardian/internal/httpauth"
	"github.com/Duang777/waybill-guardian/internal/platform"
	"github.com/Duang777/waybill-guardian/internal/tools"
)

func TestHTTPDemoFlowAndSSECursor(t *testing.T) {
	clients, _, err := tools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	service, err := guardian.Open(guardian.Config{
		DataDir:   t.TempDir(),
		Clients:   clients,
		StepDelay: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	server := httptest.NewServer(newHandler(service, newLocalAccess(t)))
	defer server.Close()

	response, err := http.Post(server.URL+"/api/demo/trigger", "application/json", http.NoBody)
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
		http.NoBody,
	)
	if err != nil {
		t.Fatal(err)
	}
	confirmRequest.Header.Set("X-Actor", "forged-reviewer")
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

func TestTimelineStopsWritingToSlowClientAfterDeadline(t *testing.T) {
	clients, _, err := tools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	service, err := guardian.Open(guardian.Config{
		DataDir:   t.TempDir(),
		Clients:   clients,
		StepDelay: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	run, err := service.StartDemo(context.Background())
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
	clients, _, err := tools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	service, err := guardian.Open(guardian.Config{
		DataDir:   t.TempDir(),
		Clients:   clients,
		StepDelay: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	run, err := service.StartDemo(context.Background())
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
	clients, _, err := tools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	service, err := guardian.Open(guardian.Config{
		DataDir:   t.TempDir(),
		Clients:   clients,
		StepDelay: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	run, err := service.StartDemo(context.Background())
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
	clients, _, err := tools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	service, err := guardian.Open(guardian.Config{DataDir: t.TempDir(), Clients: clients})
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
	clients, _, err := tools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	service, err := guardian.Open(guardian.Config{DataDir: t.TempDir(), Clients: clients})
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
	clients, _, err := tools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	service, err := guardian.Open(guardian.Config{
		DataDir:   t.TempDir(),
		Clients:   clients,
		StepDelay: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	server := httptest.NewServer(newHandler(service, newLocalAccess(t)))
	defer server.Close()

	run, err := service.StartDemo(context.Background())
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
	clients, _, err := tools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	service, err := guardian.Open(guardian.Config{DataDir: t.TempDir(), Clients: clients})
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

func TestRealPlatformFailsFast(t *testing.T) {
	t.Setenv("PLATFORM", "real")
	if _, err := platformClients(); !errors.Is(err, platform.ErrNotImplemented) {
		t.Fatalf("platformClients error = %v, want ErrNotImplemented", err)
	}
}

func TestRuntimeStorageConfiguration(t *testing.T) {
	if err := validateRuntimeModes("mock", "jsonl", httpauth.ModeLocal); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeModes("mock", "postgres", httpauth.ModeJWT); err != nil {
		t.Fatal(err)
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
		if err := validateHTTPAddr(addr, httpauth.ModeLocal); err != nil {
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
		if err := validateHTTPAddr(addr, httpauth.ModeLocal); err == nil {
			t.Errorf("validateHTTPAddr(%q) unexpectedly succeeded", addr)
		}
	}
	if err := validateHTTPAddr("0.0.0.0:8080", httpauth.ModeJWT); err != nil {
		t.Fatalf("JWT listener was rejected: %v", err)
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
