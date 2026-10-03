package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/events"
	"github.com/Duang777/waybill-guardian/internal/guardian"
	"github.com/Duang777/waybill-guardian/internal/httpauth"
	"github.com/Duang777/waybill-guardian/internal/metrics"
	"github.com/Duang777/waybill-guardian/internal/platform"
)

type api struct {
	service      *guardian.Service
	access       *httpauth.Boundary
	eventStore   events.Ingestor
	eventMetrics ingestObserver
	mux          *http.ServeMux
	sseSlots     chan struct{}
}

type ingestObserver interface {
	ObserveIngest(metrics.IngestOutcome)
}

const (
	sseWriteTimeout     = 5 * time.Second
	maxSSESubscriptions = 32
)

func newHandler(service *guardian.Service, access *httpauth.Boundary) http.Handler {
	return newHandlerWithEvents(service, access, nil, nil)
}

func newHandlerWithEvents(
	service *guardian.Service,
	access *httpauth.Boundary,
	eventStore events.Ingestor,
	eventMetrics ingestObserver,
) http.Handler {
	server := &api{
		service:      service,
		access:       access,
		eventStore:   eventStore,
		eventMetrics: eventMetrics,
		mux:          http.NewServeMux(),
		sseSlots:     make(chan struct{}, maxSSESubscriptions),
	}
	server.mux.HandleFunc("POST /api/demo/trigger", server.triggerDemo)
	server.mux.HandleFunc("GET /api/runs", server.listRuns)
	server.mux.HandleFunc("GET /api/runs/{id}", server.runSnapshot)
	server.mux.HandleFunc("GET /api/runs/{id}/timeline", server.timeline)
	server.mux.HandleFunc("GET /api/approvals", server.listApprovals)
	server.mux.HandleFunc("POST /api/approvals/{id}/confirm", server.confirm)
	server.mux.HandleFunc("POST /api/approvals/{id}/reject", server.reject)
	server.mux.HandleFunc("GET /api/waybills/{id}", server.waybill)
	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", server.health)
	root.Handle("/api/", authenticateAPI(access, server.mux))
	if eventStore != nil {
		eventMux := http.NewServeMux()
		eventMux.HandleFunc("POST /v1/events", server.ingestEvent)
		root.Handle("/v1/events", authenticateAPI(access, eventMux))
	}
	if access.Mode() == httpauth.ModeLocal {
		return loopbackHostOnly(root)
	}
	return root
}

func (a *api) ingestEvent(w http.ResponseWriter, r *http.Request) {
	_, grant, ok := a.grant(w, r, httpauth.IngestEvent)
	if !ok {
		a.observeIngest(metrics.IngestRejected)
		return
	}
	submission, err := events.DecodeStructured(events.DecodeRequest{
		ContentType:   r.Header.Get("Content-Type"),
		ContentLength: r.ContentLength,
		Body:          r.Body,
		Now:           time.Now().UTC(),
	})
	if err != nil {
		a.observeIngest(metrics.IngestRejected)
		a.writeEventError(w, err)
		return
	}
	record := submission.Record()
	if !grant.AllowsEvent(record.Ref.Source, string(record.Type)) ||
		!grant.Allows(record.WaybillID) {
		a.observeIngest(metrics.IngestRejected)
		a.writeAccessError(w, httpauth.ErrForbidden)
		return
	}
	result, err := a.eventStore.IngestEvent(r.Context(), submission)
	if err != nil {
		a.observeIngest(metrics.IngestFailed)
		a.writeEventError(w, err)
		return
	}
	body, err := result.ResponseJSON()
	if err != nil {
		a.observeIngest(metrics.IngestFailed)
		writeProblem(w, http.StatusInternalServerError, "internal_error", "request failed")
		return
	}
	if result.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		a.observeIngest(metrics.IngestReplayed)
	} else {
		a.observeIngest(metrics.IngestAccepted)
	}
	writeRawJSON(w, http.StatusAccepted, body)
}

func (a *api) observeIngest(outcome metrics.IngestOutcome) {
	if a.eventMetrics != nil {
		a.eventMetrics.ObserveIngest(outcome)
	}
}

func (a *api) writeEventError(w http.ResponseWriter, err error) {
	if code, ok := events.DecodeErrorCode(err); ok {
		status := http.StatusBadRequest
		switch code {
		case events.DecodeInvalidContentType:
			status = http.StatusUnsupportedMediaType
		case events.DecodeBodyTooLarge:
			status = http.StatusRequestEntityTooLarge
		}
		writeProblem(w, status, string(code), err.Error())
		return
	}
	switch {
	case errors.Is(err, events.ErrEventIdentityConflict):
		writeProblem(
			w,
			http.StatusConflict,
			"event_identity_conflict",
			"event identity has different content",
		)
	case errors.Is(err, events.ErrLegacyEventIdentity):
		writeProblem(
			w,
			http.StatusConflict,
			"event_identity_legacy",
			"event identity uses a legacy hash profile",
		)
	case errors.Is(err, events.ErrEventsUnavailable):
		writeProblem(
			w,
			http.StatusServiceUnavailable,
			"events_unavailable",
			"event ingestion is temporarily unavailable",
		)
	default:
		writeProblem(w, http.StatusInternalServerError, "internal_error", "request failed")
	}
}

func authenticateAPI(access *httpauth.Boundary, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authenticated, err := access.Authenticate(r)
		if err != nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeProblem(w, http.StatusUnauthorized, "unauthenticated", "valid authentication is required")
			return
		}
		principal, err := httpauth.PrincipalFrom(authenticated.Context())
		if err != nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeProblem(w, http.StatusUnauthorized, "unauthenticated", "valid authentication is required")
			return
		}
		if deadline, ok := principal.CredentialDeadline(); ok {
			ctx, cancel := context.WithDeadline(authenticated.Context(), deadline)
			defer cancel()
			authenticated = authenticated.WithContext(ctx)
		}
		next.ServeHTTP(w, authenticated)
	})
}

func loopbackHostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := validateRequestHost(r.Host); err != nil {
			writeProblem(w, http.StatusForbidden, "invalid_host", "request host must be a loopback IP address")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *api) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *api) triggerDemo(w http.ResponseWriter, r *http.Request) {
	_, grant, ok := a.grant(w, r, httpauth.StartRun)
	if !ok || !a.requireWaybill(w, grant, guardian.DemoWaybillID) {
		return
	}
	if err := requireEmptyBody(r.Body); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	run, err := a.service.StartDemo(r.Context())
	if err != nil {
		a.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (a *api) listRuns(w http.ResponseWriter, r *http.Request) {
	_, grant, ok := a.grant(w, r, httpauth.Read)
	if !ok {
		return
	}
	if err := requireFilter(r, "active"); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	runs, err := a.service.ListActiveRuns(r.Context())
	if err != nil {
		a.writeServiceError(w, err)
		return
	}
	runs = slices.DeleteFunc(runs, func(run guardian.RunSummary) bool {
		return !grant.Allows(run.WaybillID)
	})
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (a *api) runSnapshot(w http.ResponseWriter, r *http.Request) {
	_, grant, ok := a.grant(w, r, httpauth.Read)
	if !ok {
		return
	}
	runID := domain.RunID(r.PathValue("id"))
	if _, ok := a.authorizeRun(w, grant, runID); !ok {
		return
	}
	snapshot, err := a.service.Snapshot(r.Context(), runID)
	if err != nil {
		a.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (a *api) listApprovals(w http.ResponseWriter, r *http.Request) {
	_, grant, ok := a.grant(w, r, httpauth.Read)
	if !ok {
		return
	}
	if err := requireFilter(r, "pending"); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	approvals, err := a.service.ListPendingApprovals(r.Context())
	if err != nil {
		a.writeServiceError(w, err)
		return
	}
	approvals = slices.DeleteFunc(approvals, func(value guardian.PendingApprovalSummary) bool {
		return !grant.Allows(value.WaybillID)
	})
	writeJSON(w, http.StatusOK, map[string]any{"approvals": approvals})
}

func (a *api) waybill(w http.ResponseWriter, r *http.Request) {
	id := domain.WaybillID(r.PathValue("id"))
	if err := domain.ValidateWaybillID(id); err != nil {
		a.writeServiceError(w, err)
		return
	}
	_, grant, ok := a.grant(w, r, httpauth.Read)
	if !ok || !a.requireWaybill(w, grant, id) {
		return
	}
	view, err := a.service.GetWaybill(r.Context(), id)
	if err != nil {
		a.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *api) confirm(w http.ResponseWriter, r *http.Request) {
	principal, grant, ok := a.grant(w, r, httpauth.DecideApproval)
	if !ok {
		return
	}
	if err := requireEmptyBody(r.Body); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	id := domain.ApprovalID(r.PathValue("id"))
	if _, ok := a.authorizeApproval(w, grant, id); !ok {
		return
	}
	value, err := a.service.Decide(r.Context(), id, guardian.DecisionRequest{
		Kind:      approval.DecisionConfirm,
		DecidedBy: principal.Subject(),
	})
	if err != nil {
		a.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, value)
}

func (a *api) reject(w http.ResponseWriter, r *http.Request) {
	principal, grant, ok := a.grant(w, r, httpauth.DecideApproval)
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	id := domain.ApprovalID(r.PathValue("id"))
	if _, ok := a.authorizeApproval(w, grant, id); !ok {
		return
	}
	value, err := a.service.Decide(r.Context(), id, guardian.DecisionRequest{
		Kind:         approval.DecisionReject,
		DecidedBy:    principal.Subject(),
		RejectReason: strings.TrimSpace(body.Reason),
	})
	if err != nil {
		a.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, value)
}

func (a *api) timeline(w http.ResponseWriter, r *http.Request) {
	_, grant, ok := a.grant(w, r, httpauth.Read)
	if !ok {
		return
	}
	runID := domain.RunID(r.PathValue("id"))
	if _, ok := a.authorizeRun(w, grant, runID); !ok {
		return
	}
	if !a.acquireSSESlot() {
		writeProblem(w, http.StatusTooManyRequests, "too_many_streams", "too many active timeline streams")
		return
	}
	defer a.releaseSSESlot()

	after, err := parseTimelineCursor(r)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_cursor", err.Error())
		return
	}
	subscription, err := a.service.Timeline(r.Context(), runID, after)
	if err != nil {
		a.writeServiceError(w, err)
		return
	}
	defer subscription.Close()

	if _, ok := w.(http.Flusher); !ok {
		writeProblem(w, http.StatusInternalServerError, "stream_unsupported", "response writer cannot stream")
		return
	}
	controller := http.NewResponseController(w)
	if err := prepareSSEWrite(r.Context(), controller); err != nil {
		if r.Context().Err() != nil {
			return
		}
		writeProblem(w, http.StatusInternalServerError, "stream_unsupported", "response writer cannot set deadlines")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := controller.Flush(); err != nil {
		return
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		if r.Context().Err() != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if err := prepareSSEWrite(r.Context(), controller); err != nil {
				return
			}
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		case event, open := <-subscription.Events():
			if !open {
				return
			}
			raw, err := json.Marshal(event)
			if err != nil {
				return
			}
			if err := prepareSSEWrite(r.Context(), controller); err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Seq, event.Type, raw); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		}
	}
}

func (a *api) grant(
	w http.ResponseWriter,
	r *http.Request,
	capability httpauth.Capability,
) (httpauth.Principal, httpauth.Grant, bool) {
	principal, err := httpauth.PrincipalFrom(r.Context())
	if err != nil {
		a.writeAccessError(w, err)
		return httpauth.Principal{}, httpauth.Grant{}, false
	}
	grant, err := a.access.Grant(principal, capability)
	if err != nil {
		a.writeAccessError(w, err)
		return httpauth.Principal{}, httpauth.Grant{}, false
	}
	return principal, grant, true
}

func (a *api) authorizeRun(
	w http.ResponseWriter,
	grant httpauth.Grant,
	id domain.RunID,
) (guardian.RunView, bool) {
	run, err := a.service.GetRun(id)
	if err != nil {
		a.writeServiceError(w, err)
		return guardian.RunView{}, false
	}
	if !a.requireWaybill(w, grant, run.WaybillID) {
		return guardian.RunView{}, false
	}
	return run, true
}

func (a *api) authorizeApproval(
	w http.ResponseWriter,
	grant httpauth.Grant,
	id domain.ApprovalID,
) (approval.Approval, bool) {
	value, err := a.service.GetApproval(id)
	if err != nil {
		a.writeServiceError(w, err)
		return approval.Approval{}, false
	}
	if !a.requireWaybill(w, grant, value.WaybillID) {
		return approval.Approval{}, false
	}
	return value, true
}

func (a *api) requireWaybill(
	w http.ResponseWriter,
	grant httpauth.Grant,
	id domain.WaybillID,
) bool {
	if err := grant.Require(id); err != nil {
		a.writeAccessError(w, err)
		return false
	}
	return true
}

func (a *api) writeAccessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, httpauth.ErrUnauthenticated):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeProblem(w, http.StatusUnauthorized, "unauthenticated", "valid authentication is required")
	default:
		writeProblem(w, http.StatusForbidden, "forbidden", "access is forbidden")
	}
}

func (a *api) acquireSSESlot() bool {
	if a.sseSlots == nil {
		return true
	}
	select {
	case a.sseSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (a *api) releaseSSESlot() {
	if a.sseSlots != nil {
		<-a.sseSlots
	}
}

func prepareSSEWrite(ctx context.Context, controller *http.ResponseController) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := setSSEWriteDeadline(ctx, controller); err != nil {
		return err
	}
	return ctx.Err()
}

func setSSEWriteDeadline(ctx context.Context, controller *http.ResponseController) error {
	deadline := time.Now().Add(sseWriteTimeout)
	if requestDeadline, ok := ctx.Deadline(); ok && requestDeadline.Before(deadline) {
		deadline = requestDeadline
	}
	return controller.SetWriteDeadline(deadline)
}

func (a *api) writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidWaybillID):
		writeProblem(w, http.StatusBadRequest, "invalid_waybill_id", err.Error())
	case errors.Is(err, platform.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", err.Error())
	case guardian.IsNotFound(err):
		writeProblem(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, approval.ErrDecisionConflict):
		writeProblem(w, http.StatusConflict, "decision_conflict", err.Error())
	case errors.Is(err, approval.ErrRejectReason):
		writeProblem(w, http.StatusBadRequest, "reject_reason_required", err.Error())
	case errors.Is(err, audit.ErrCursorAhead):
		writeProblem(w, http.StatusConflict, "cursor_ahead", err.Error())
	case errors.Is(err, guardian.ErrServiceClosed):
		writeProblem(w, http.StatusServiceUnavailable, "service_closing", "service is shutting down")
	default:
		writeProblem(w, http.StatusInternalServerError, "internal_error", "request failed")
	}
}

func parseLastEventID(value string) (audit.Seq, error) {
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("Last-Event-ID must be an unsigned integer")
	}
	return audit.Seq(parsed), nil
}

func parseTimelineCursor(r *http.Request) (audit.Seq, error) {
	if header := r.Header.Get("Last-Event-ID"); header != "" {
		return parseLastEventID(header)
	}
	values, ok := r.URL.Query()["after"]
	if !ok {
		return 0, nil
	}
	if len(values) != 1 || values[0] == "" {
		return 0, fmt.Errorf("after must be one unsigned integer")
	}
	return parseLastEventID(values[0])
}

func requireFilter(r *http.Request, expected string) error {
	query := r.URL.Query()
	values, ok := query["status"]
	if len(query) != 1 || !ok || len(values) != 1 || values[0] != expected {
		return fmt.Errorf("status must be %q", expected)
	}
	return nil
}

func requireEmptyBody(body io.Reader) error {
	decoder := json.NewDecoder(body)
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("request body must be empty")
	}
	return fmt.Errorf("request body must be empty")
}

func decodeJSON(body io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeRawJSON(w http.ResponseWriter, status int, value []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(value)
}

func writeProblem(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
