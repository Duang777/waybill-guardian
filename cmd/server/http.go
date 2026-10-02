package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/guardian"
)

type api struct {
	service *guardian.Service
	mux     *http.ServeMux
}

func newHandler(service *guardian.Service) http.Handler {
	server := &api{service: service, mux: http.NewServeMux()}
	server.mux.HandleFunc("GET /healthz", server.health)
	server.mux.HandleFunc("POST /api/demo/trigger", server.triggerDemo)
	server.mux.HandleFunc("GET /api/runs/{id}/timeline", server.timeline)
	server.mux.HandleFunc("POST /api/approvals/{id}/confirm", server.confirm)
	server.mux.HandleFunc("POST /api/approvals/{id}/reject", server.reject)
	server.mux.HandleFunc("GET /api/waybills/{id}", server.waybill)
	return server.mux
}

func (a *api) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *api) triggerDemo(w http.ResponseWriter, r *http.Request) {
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

func (a *api) waybill(w http.ResponseWriter, r *http.Request) {
	view, err := a.service.GetWaybill(r.Context(), domain.WaybillID(r.PathValue("id")))
	if err != nil {
		a.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *api) confirm(w http.ResponseWriter, r *http.Request) {
	if err := requireEmptyBody(r.Body); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	value, err := a.service.Decide(r.Context(), domain.ApprovalID(r.PathValue("id")), guardian.DecisionRequest{
		Kind:      approval.DecisionConfirm,
		DecidedBy: actorFromRequest(r),
	})
	if err != nil {
		a.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (a *api) reject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	value, err := a.service.Decide(r.Context(), domain.ApprovalID(r.PathValue("id")), guardian.DecisionRequest{
		Kind:         approval.DecisionReject,
		DecidedBy:    actorFromRequest(r),
		RejectReason: strings.TrimSpace(body.Reason),
	})
	if err != nil {
		a.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (a *api) timeline(w http.ResponseWriter, r *http.Request) {
	after, err := parseLastEventID(r.Header.Get("Last-Event-ID"))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_cursor", err.Error())
		return
	}
	subscription, err := a.service.Timeline(r.Context(), domain.RunID(r.PathValue("id")), after)
	if err != nil {
		a.writeServiceError(w, err)
		return
	}
	defer subscription.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeProblem(w, http.StatusInternalServerError, "stream_unsupported", "response writer cannot stream")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case event, open := <-subscription.Events():
			if !open {
				return
			}
			raw, err := json.Marshal(event)
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Seq, event.Type, raw); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (a *api) writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case guardian.IsNotFound(err):
		writeProblem(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, approval.ErrDecisionConflict):
		writeProblem(w, http.StatusConflict, "decision_conflict", err.Error())
	case errors.Is(err, approval.ErrRejectReason):
		writeProblem(w, http.StatusBadRequest, "reject_reason_required", err.Error())
	case errors.Is(err, audit.ErrCursorAhead):
		writeProblem(w, http.StatusConflict, "cursor_ahead", err.Error())
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

func actorFromRequest(r *http.Request) string {
	if actor := strings.TrimSpace(r.Header.Get("X-Actor")); actor != "" {
		return actor
	}
	return "demo-reviewer"
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

func writeProblem(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
