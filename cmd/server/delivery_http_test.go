package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	deliveryartifact "github.com/Duang777/waybill-guardian/internal/delivery/artifact"
	deliverydomain "github.com/Duang777/waybill-guardian/internal/delivery/domain"
	deliveryservice "github.com/Duang777/waybill-guardian/internal/delivery/service"
)

func TestDeliveryHTTPUsesAuthenticatedTenantAndRejectsAuthorityFields(t *testing.T) {
	guardianService := newSimulatedHTTPService(t, 1)
	defer guardianService.Close()
	platform := &deliveryHTTPPlatform{}
	server := httptest.NewServer(newHandlerWithFrontendAndDelivery(
		guardianService,
		newLocalAccess(t),
		nil,
		nil,
		nil,
		nil,
		defaultCrossOriginProtection(),
		platform,
	))
	defer server.Close()

	request, err := http.NewRequest(
		http.MethodPost,
		server.URL+"/api/v1/delivery/problems",
		bytes.NewBufferString(`{
			"tenant_id":"attacker",
			"problem_id":"problem-1",
			"source_profile":"postgres-v1",
			"snapshot_ref":"cut-1",
			"horizon":{
				"start":"2026-10-10T08:00:00Z",
				"end":"2026-10-10T16:00:00Z"
			}
		}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "problem-key")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("authority field status = %d, want 400", response.StatusCode)
	}
	if platform.createCalls != 0 {
		t.Fatalf("create calls = %d, want 0", platform.createCalls)
	}

	request, err = http.NewRequest(
		http.MethodPost,
		server.URL+"/api/v1/delivery/problems",
		bytes.NewBufferString(`{
			"problem_id":"problem-1",
			"source_profile":"postgres-v1",
			"snapshot_ref":"cut-1",
			"horizon":{
				"start":"2026-10-10T08:00:00Z",
				"end":"2026-10-10T16:00:00Z"
			}
		}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "problem-key")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("create status = %d body=%s", response.StatusCode, raw)
	}
	if platform.createCalls != 1 ||
		platform.created.TenantID != "local-demo" ||
		platform.created.Actor.Subject != "local-demo-reviewer" ||
		platform.created.Version != 1 {
		t.Fatalf("create command = %+v, calls=%d",
			platform.created, platform.createCalls)
	}
	if len(platform.created.Commitments.Executed) != 0 ||
		platform.created.Commitments.Executed == nil {
		t.Fatalf("server-owned commitments = %+v", platform.created.Commitments)
	}
}

func TestDeliveryHTTPRequiresIdempotencyAndIfMatch(t *testing.T) {
	guardianService := newSimulatedHTTPService(t, 1)
	defer guardianService.Close()
	platform := &deliveryHTTPPlatform{}
	server := httptest.NewServer(newHandlerWithFrontendAndDelivery(
		guardianService,
		newLocalAccess(t),
		nil,
		nil,
		nil,
		nil,
		defaultCrossOriginProtection(),
		platform,
	))
	defer server.Close()

	request, err := http.NewRequest(
		http.MethodPost,
		server.URL+"/api/v1/delivery/problems/problem-1/runs",
		bytes.NewBufferString(`{"problem_version":1,"solver_profile":"solver-v1"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing idempotency status = %d, want 400", response.StatusCode)
	}

	request, err = http.NewRequest(
		http.MethodPost,
		server.URL+"/api/v1/delivery/runs/run-1/cancel",
		bytes.NewBufferString(`{}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match status = %d, want 428", response.StatusCode)
	}

	request, err = http.NewRequest(
		http.MethodPost,
		server.URL+"/api/v1/delivery/runs/run-1/cancel",
		bytes.NewBufferString(`{}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"3"`)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("cancel status = %d, want 202", response.StatusCode)
	}
	if platform.cancelled.TenantID != "local-demo" ||
		platform.cancelled.ExpectedVersion != 3 ||
		platform.cancelled.RunID != "run-1" {
		t.Fatalf("cancel command = %+v", platform.cancelled)
	}
}

func TestDeliveryHTTPApprovalBoundaryRejectsAuthoritativeFields(t *testing.T) {
	guardianService := newSimulatedHTTPService(t, 1)
	defer guardianService.Close()
	platform := &deliveryHTTPPlatform{}
	server := httptest.NewServer(newHandlerWithFrontendAndDelivery(
		guardianService,
		newLocalAccess(t),
		nil,
		nil,
		nil,
		nil,
		defaultCrossOriginProtection(),
		platform,
	))
	defer server.Close()

	request, err := http.NewRequest(
		http.MethodPost,
		server.URL+"/api/v1/delivery/revisions/revision-1/approval-requests",
		bytes.NewBufferString(`{
			"ttl_seconds":600,
			"reason":"dispatch reviewed candidate",
			"effect_set_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "approval-key")
	request.Header.Set("If-Match", `"4"`)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || platform.approvalCalls != 0 {
		t.Fatalf(
			"authority request status=%d calls=%d",
			response.StatusCode,
			platform.approvalCalls,
		)
	}

	request, err = http.NewRequest(
		http.MethodPost,
		server.URL+"/api/v1/delivery/revisions/revision-1/approval-requests",
		bytes.NewBufferString(`{
			"ttl_seconds":9223372036854775807,
			"reason":"overflow duration"
		}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "approval-key")
	request.Header.Set("If-Match", `"4"`)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || platform.approvalCalls != 0 {
		t.Fatalf(
			"overflow request status=%d calls=%d",
			response.StatusCode,
			platform.approvalCalls,
		)
	}

	request, err = http.NewRequest(
		http.MethodPost,
		server.URL+"/api/v1/delivery/revisions/revision-1/approval-requests",
		bytes.NewBufferString(`{
			"ttl_seconds":600,
			"reason":"dispatch reviewed candidate"
		}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "approval-key")
	request.Header.Set("If-Match", `"4"`)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("approval request status = %d, want 201", response.StatusCode)
	}
	if platform.approvalRequest.TenantID != "local-demo" ||
		platform.approvalRequest.Actor.Subject != "local-demo-reviewer" ||
		platform.approvalRequest.RevisionID != "revision-1" ||
		platform.approvalRequest.ExpectedVersion != 4 ||
		platform.approvalRequest.TTL != 10*time.Minute ||
		platform.approvalRequest.Reason != "dispatch reviewed candidate" {
		t.Fatalf("approval request = %+v", platform.approvalRequest)
	}

	request, err = http.NewRequest(
		http.MethodPost,
		server.URL+"/api/v1/delivery/approvals/approval-1/decisions",
		bytes.NewBufferString(`{
			"decision":"confirm",
			"effect_id":"attacker-effect"
		}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "decision-key")
	request.Header.Set("If-Match", `"1"`)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || platform.decisionCalls != 0 {
		t.Fatalf(
			"authority decision status=%d calls=%d",
			response.StatusCode,
			platform.decisionCalls,
		)
	}

	request, err = http.NewRequest(
		http.MethodPost,
		server.URL+"/api/v1/delivery/approvals/approval-1/decisions",
		bytes.NewBufferString(`{"decision":"confirm"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "decision-key")
	request.Header.Set("If-Match", `"1"`)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("decision status = %d, want 202", response.StatusCode)
	}
	if platform.decision.TenantID != "local-demo" ||
		platform.decision.Actor.Subject != "local-demo-reviewer" ||
		platform.decision.ApprovalID != "approval-1" ||
		platform.decision.ExpectedVersion != 1 {
		t.Fatalf("decision = %+v", platform.decision)
	}
}

type deliveryHTTPPlatform struct {
	createCalls     int
	approvalCalls   int
	decisionCalls   int
	created         deliveryservice.CreateProblem
	cancelled       deliveryservice.CancelOptimization
	approvalRequest deliveryservice.RequestPlanApproval
	decision        deliveryservice.DecideApproval
}

func (platform *deliveryHTTPPlatform) Commands() deliveryservice.Commands {
	return platform
}

func (platform *deliveryHTTPPlatform) Queries() deliveryservice.Queries {
	return platform
}

func (platform *deliveryHTTPPlatform) EventStreams() deliveryservice.EventStreams {
	return platform
}

func (platform *deliveryHTTPPlatform) RunWorker(
	deliveryservice.WorkerConfig,
) (*deliveryservice.RunWorker, error) {
	return nil, errors.New("not used")
}

func (platform *deliveryHTTPPlatform) EffectWorker(
	deliveryservice.EffectWorkerConfig,
) (*deliveryservice.EffectWorker, error) {
	return nil, errors.New("not used")
}

func (platform *deliveryHTTPPlatform) Recover(context.Context) error {
	return nil
}

func (platform *deliveryHTTPPlatform) CreateProblem(
	_ context.Context,
	command deliveryservice.CreateProblem,
) (deliverydomain.ProblemVersion, deliveryservice.Replay, error) {
	platform.createCalls++
	platform.created = command
	return deliverydomain.ProblemVersion{
		TenantID:  command.TenantID,
		ProblemID: command.ProblemID,
		Version:   command.Version,
		CreatedAt: time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC),
	}, deliveryservice.Replay{}, nil
}

func (platform *deliveryHTTPPlatform) RequestOptimization(
	_ context.Context,
	command deliveryservice.RequestOptimization,
) (deliverydomain.OptimizationRun, deliveryservice.Replay, error) {
	return deliverydomain.OptimizationRun{
		TenantID: command.TenantID,
		ID:       "run-1",
		Status:   deliverydomain.RunQueued,
		Version:  1,
	}, deliveryservice.Replay{}, nil
}

func (platform *deliveryHTTPPlatform) CancelOptimization(
	_ context.Context,
	command deliveryservice.CancelOptimization,
) (deliverydomain.OptimizationRun, error) {
	platform.cancelled = command
	return deliverydomain.OptimizationRun{
		TenantID: command.TenantID,
		ID:       command.RunID,
		Status:   deliverydomain.RunCancelled,
		Version:  command.ExpectedVersion + 1,
	}, nil
}

func (platform *deliveryHTTPPlatform) RequestPlanApproval(
	_ context.Context,
	request deliveryservice.RequestPlanApproval,
) (deliverydomain.PlanApproval, deliveryservice.Replay, error) {
	platform.approvalCalls++
	platform.approvalRequest = request
	return deliverydomain.PlanApproval{
		TenantID: request.TenantID,
		ID:       "approval-1",
		Status:   deliverydomain.ApprovalPending,
		Version:  1,
	}, deliveryservice.Replay{}, nil
}

func (platform *deliveryHTTPPlatform) DecideApproval(
	_ context.Context,
	request deliveryservice.DecideApproval,
) (deliverydomain.DispatchExecution, deliveryservice.Replay, error) {
	platform.decisionCalls++
	platform.decision = request
	return deliverydomain.DispatchExecution{
		TenantID:   request.TenantID,
		ID:         "execution-1",
		ApprovalID: request.ApprovalID,
		Status:     deliverydomain.ExecutionPrepared,
		Version:    1,
	}, deliveryservice.Replay{}, nil
}

func (platform *deliveryHTTPPlatform) GetProblem(
	context.Context,
	deliverydomain.TenantID,
	deliverydomain.ProblemID,
	uint64,
) (deliverydomain.ProblemVersion, error) {
	return deliverydomain.ProblemVersion{}, deliveryservice.ErrNotFound
}

func (platform *deliveryHTTPPlatform) GetRun(
	context.Context,
	deliverydomain.TenantID,
	deliverydomain.OptimizationRunID,
) (deliverydomain.OptimizationRun, error) {
	return deliverydomain.OptimizationRun{}, deliveryservice.ErrNotFound
}

func (platform *deliveryHTTPPlatform) GetPlan(
	context.Context,
	deliverydomain.TenantID,
	deliverydomain.PlanID,
) (deliverydomain.DispatchPlan, error) {
	return deliverydomain.DispatchPlan{}, deliveryservice.ErrNotFound
}

func (platform *deliveryHTTPPlatform) GetRevision(
	context.Context,
	deliverydomain.TenantID,
	deliverydomain.PlanRevisionID,
) (deliverydomain.PlanRevision, error) {
	return deliverydomain.PlanRevision{}, deliveryservice.ErrNotFound
}

func (platform *deliveryHTTPPlatform) GetApproval(
	context.Context,
	deliverydomain.TenantID,
	deliverydomain.ApprovalID,
) (deliverydomain.PlanApproval, error) {
	return deliverydomain.PlanApproval{}, deliveryservice.ErrNotFound
}

func (platform *deliveryHTTPPlatform) GetExecution(
	context.Context,
	deliverydomain.TenantID,
	deliverydomain.ExecutionID,
) (deliverydomain.DispatchExecution, []deliverydomain.EffectRecord, error) {
	return deliverydomain.DispatchExecution{}, nil, deliveryservice.ErrNotFound
}

func (platform *deliveryHTTPPlatform) OpenArtifact(
	context.Context,
	deliverydomain.TenantID,
	deliverydomain.ArtifactDigest,
) (io.ReadCloser, deliveryartifact.Metadata, error) {
	return nil, deliveryartifact.Metadata{}, deliveryservice.ErrNotFound
}

func (platform *deliveryHTTPPlatform) Replay(
	context.Context,
	deliveryservice.StreamCursor,
) ([]deliveryservice.Event, error) {
	return nil, deliveryservice.ErrNotFound
}

func (platform *deliveryHTTPPlatform) Subscribe(
	context.Context,
	deliveryservice.StreamCursor,
) (deliveryservice.Subscription, error) {
	return nil, deliveryservice.ErrNotFound
}

var _ deliveryservice.Platform = (*deliveryHTTPPlatform)(nil)
