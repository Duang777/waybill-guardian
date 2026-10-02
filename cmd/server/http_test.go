package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/guardian"
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
	server := httptest.NewServer(newHandler(service))
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

	confirmResponse, err := http.Post(
		server.URL+"/api/approvals/"+string(pending.ID)+"/confirm",
		"application/json",
		http.NoBody,
	)
	if err != nil {
		t.Fatal(err)
	}
	if confirmResponse.StatusCode != http.StatusOK {
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
	server := httptest.NewServer(newHandler(service))
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

func TestParseLastEventID(t *testing.T) {
	if value, err := parseLastEventID(""); err != nil || value != 0 {
		t.Fatalf("empty cursor = %d, %v", value, err)
	}
	if _, err := parseLastEventID("-1"); err == nil {
		t.Fatal("negative cursor was accepted")
	}
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
