package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	deliveryartifact "github.com/Duang777/waybill-guardian/internal/delivery/artifact"
	deliverydomain "github.com/Duang777/waybill-guardian/internal/delivery/domain"
	deliveryservice "github.com/Duang777/waybill-guardian/internal/delivery/service"
	"github.com/Duang777/waybill-guardian/internal/httpauth"
)

func (a *api) mountDeliveryRoutes() {
	a.mux.HandleFunc("POST /api/v1/delivery/problems", a.createDeliveryProblem)
	a.mux.HandleFunc("GET /api/v1/delivery/problems/{id}", a.getDeliveryProblem)
	a.mux.HandleFunc(
		"POST /api/v1/delivery/problems/{id}/runs",
		a.requestDeliveryOptimization,
	)
	a.mux.HandleFunc("GET /api/v1/delivery/runs/{id}", a.getDeliveryRun)
	a.mux.HandleFunc("POST /api/v1/delivery/runs/{id}/cancel", a.cancelDeliveryRun)
	a.mux.HandleFunc("GET /api/v1/delivery/runs/{id}/candidate", a.getDeliveryCandidate)
	a.mux.HandleFunc("GET /api/v1/delivery/plans/{id}", a.getDeliveryPlan)
	a.mux.HandleFunc("GET /api/v1/delivery/revisions/{id}", a.getDeliveryRevision)
	a.mux.HandleFunc(
		"POST /api/v1/delivery/revisions/{id}/approval-requests",
		a.requestDeliveryPlanApproval,
	)
	a.mux.HandleFunc("GET /api/v1/delivery/approvals/{id}", a.getDeliveryApproval)
	a.mux.HandleFunc(
		"POST /api/v1/delivery/approvals/{id}/decisions",
		a.decideDeliveryApproval,
	)
	a.mux.HandleFunc("GET /api/v1/delivery/executions/{id}", a.getDeliveryExecution)
	a.mux.HandleFunc("GET /api/v1/delivery/artifacts/{digest}", a.getDeliveryArtifact)
	a.mux.HandleFunc("GET /api/v1/delivery/problems/{id}/events", a.deliveryProblemEvents)
	a.mux.HandleFunc("GET /api/v1/delivery/runs/{id}/events", a.deliveryRunEvents)
	a.mux.HandleFunc("GET /api/v1/delivery/plans/{id}/events", a.deliveryPlanEvents)
	a.mux.HandleFunc("GET /api/v1/delivery/revisions/{id}/events", a.deliveryRevisionEvents)
	a.mux.HandleFunc("GET /api/v1/delivery/executions/{id}/events", a.deliveryExecutionEvents)
}

func (a *api) createDeliveryProblem(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := a.grant(w, r, httpauth.DeliveryProblemWrite)
	if !ok {
		return
	}
	if err := requireJSONContentType(r.Header.Get("Content-Type")); err != nil {
		writeProblem(w, http.StatusUnsupportedMediaType, "invalid_content_type", err.Error())
		return
	}
	key, err := deliveryIdempotencyKey(r)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_idempotency_key", err.Error())
		return
	}
	var body struct {
		ProblemID     deliverydomain.ProblemID `json:"problem_id"`
		SourceProfile string                   `json:"source_profile"`
		SnapshotRef   string                   `json:"snapshot_ref"`
		Horizon       deliverydomain.TimeRange `json:"horizon"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	value, replay, err := a.delivery.Commands().CreateProblem(
		r.Context(),
		deliveryservice.CreateProblem{
			TenantID:       deliverydomain.TenantID(principal.TenantID()),
			Actor:          deliveryservice.Actor{Subject: principal.Subject()},
			IdempotencyKey: key,
			ProblemID:      body.ProblemID,
			Version:        1,
			SourceProfile:  strings.TrimSpace(body.SourceProfile),
			SnapshotRef:    strings.TrimSpace(body.SnapshotRef),
			Horizon:        body.Horizon,
			Commitments: deliverydomain.CommitmentSet{
				Executed:  []deliverydomain.ExecutedTaskCommitment{},
				Frozen:    []deliverydomain.FrozenTaskCommitment{},
				InTransit: []deliverydomain.InTransitCargoCommitment{},
				Soft:      []deliverydomain.SoftTaskCommitment{},
			},
		},
	)
	if err != nil {
		writeDeliveryError(w, err)
		return
	}
	if replay.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("ETag", quotedVersion(value.Version))
	writeJSON(w, http.StatusCreated, value)
}

func (a *api) getDeliveryProblem(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := a.grant(w, r, httpauth.DeliveryRead)
	if !ok {
		return
	}
	version, err := requireUintQuery(r, "version")
	if err != nil || version == 0 {
		writeProblem(w, http.StatusBadRequest, "invalid_version", "version must be a positive integer")
		return
	}
	value, err := a.delivery.Queries().GetProblem(
		r.Context(),
		deliverydomain.TenantID(principal.TenantID()),
		deliverydomain.ProblemID(r.PathValue("id")),
		version,
	)
	if err != nil {
		writeDeliveryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (a *api) requestDeliveryOptimization(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := a.grant(w, r, httpauth.DeliveryOptimize)
	if !ok {
		return
	}
	if err := requireJSONContentType(r.Header.Get("Content-Type")); err != nil {
		writeProblem(w, http.StatusUnsupportedMediaType, "invalid_content_type", err.Error())
		return
	}
	key, err := deliveryIdempotencyKey(r)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_idempotency_key", err.Error())
		return
	}
	var body struct {
		ProblemVersion uint64 `json:"problem_version"`
		SolverProfile  string `json:"solver_profile"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	value, replay, err := a.delivery.Commands().RequestOptimization(
		r.Context(),
		deliveryservice.RequestOptimization{
			TenantID:       deliverydomain.TenantID(principal.TenantID()),
			Actor:          deliveryservice.Actor{Subject: principal.Subject()},
			IdempotencyKey: key,
			ProblemID:      deliverydomain.ProblemID(r.PathValue("id")),
			ProblemVersion: body.ProblemVersion,
			SolverProfile:  strings.TrimSpace(body.SolverProfile),
		},
	)
	if err != nil {
		writeDeliveryError(w, err)
		return
	}
	if replay.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("ETag", quotedVersion(value.Version))
	writeJSON(w, http.StatusAccepted, value)
}

func (a *api) getDeliveryRun(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := a.grant(w, r, httpauth.DeliveryRead)
	if !ok {
		return
	}
	value, err := a.delivery.Queries().GetRun(
		r.Context(),
		deliverydomain.TenantID(principal.TenantID()),
		deliverydomain.OptimizationRunID(r.PathValue("id")),
	)
	if err != nil {
		writeDeliveryError(w, err)
		return
	}
	w.Header().Set("ETag", quotedVersion(value.Version))
	writeJSON(w, http.StatusOK, value)
}

func (a *api) cancelDeliveryRun(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := a.grant(w, r, httpauth.DeliveryOptimize)
	if !ok {
		return
	}
	expected, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		writeProblem(w, http.StatusPreconditionRequired, "if_match_required", err.Error())
		return
	}
	if err := requireJSONContentType(r.Header.Get("Content-Type")); err != nil {
		writeProblem(w, http.StatusUnsupportedMediaType, "invalid_content_type", err.Error())
		return
	}
	if err := requireEmptyJSONObject(r.Body); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	value, err := a.delivery.Commands().CancelOptimization(
		r.Context(),
		deliveryservice.CancelOptimization{
			TenantID:        deliverydomain.TenantID(principal.TenantID()),
			Actor:           deliveryservice.Actor{Subject: principal.Subject()},
			RunID:           deliverydomain.OptimizationRunID(r.PathValue("id")),
			ExpectedVersion: expected,
		},
	)
	if err != nil {
		writeDeliveryError(w, err)
		return
	}
	w.Header().Set("ETag", quotedVersion(value.Version))
	writeJSON(w, http.StatusAccepted, value)
}

func (a *api) getDeliveryCandidate(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := a.grant(w, r, httpauth.DeliveryRead)
	if !ok {
		return
	}
	tenantID := deliverydomain.TenantID(principal.TenantID())
	run, err := a.delivery.Queries().GetRun(
		r.Context(),
		tenantID,
		deliverydomain.OptimizationRunID(r.PathValue("id")),
	)
	if err != nil {
		writeDeliveryError(w, err)
		return
	}
	if run.ResultRevisionID == "" {
		writeProblem(w, http.StatusConflict, "candidate_not_ready", "run has no candidate revision")
		return
	}
	revision, err := a.delivery.Queries().GetRevision(
		r.Context(),
		tenantID,
		run.ResultRevisionID,
	)
	if err != nil {
		writeDeliveryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, revision)
}

func (a *api) getDeliveryPlan(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := a.grant(w, r, httpauth.DeliveryRead)
	if !ok {
		return
	}
	value, err := a.delivery.Queries().GetPlan(
		r.Context(),
		deliverydomain.TenantID(principal.TenantID()),
		deliverydomain.PlanID(r.PathValue("id")),
	)
	if err != nil {
		writeDeliveryError(w, err)
		return
	}
	w.Header().Set("ETag", quotedVersion(value.ActiveVersion))
	writeJSON(w, http.StatusOK, value)
}

func (a *api) getDeliveryRevision(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := a.grant(w, r, httpauth.DeliveryRead)
	if !ok {
		return
	}
	value, err := a.delivery.Queries().GetRevision(
		r.Context(),
		deliverydomain.TenantID(principal.TenantID()),
		deliverydomain.PlanRevisionID(r.PathValue("id")),
	)
	if err != nil {
		writeDeliveryError(w, err)
		return
	}
	w.Header().Set("ETag", quotedVersion(value.Version))
	writeJSON(w, http.StatusOK, value)
}

func (a *api) requestDeliveryPlanApproval(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := a.grant(w, r, httpauth.DeliveryOptimize)
	if !ok {
		return
	}
	if err := requireJSONContentType(r.Header.Get("Content-Type")); err != nil {
		writeProblem(w, http.StatusUnsupportedMediaType, "invalid_content_type", err.Error())
		return
	}
	key, err := deliveryIdempotencyKey(r)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_idempotency_key", err.Error())
		return
	}
	expected, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		writeProblem(w, http.StatusPreconditionRequired, "if_match_required", err.Error())
		return
	}
	var body struct {
		TTLSeconds int64  `json:"ttl_seconds"`
		Reason     string `json:"reason"`
	}
	if err := decodeJSON(r.Body, &body); err != nil ||
		body.TTLSeconds <= 0 ||
		body.TTLSeconds > int64((24*time.Hour)/time.Second) ||
		strings.TrimSpace(body.Reason) == "" {
		writeProblem(
			w,
			http.StatusBadRequest,
			"invalid_body",
			"ttl_seconds and reason are required",
		)
		return
	}
	value, replay, err := a.delivery.Commands().RequestPlanApproval(
		r.Context(),
		deliveryservice.RequestPlanApproval{
			TenantID:        deliverydomain.TenantID(principal.TenantID()),
			Actor:           deliveryservice.Actor{Subject: principal.Subject()},
			IdempotencyKey:  key,
			RevisionID:      deliverydomain.PlanRevisionID(r.PathValue("id")),
			ExpectedVersion: expected,
			TTL:             time.Duration(body.TTLSeconds) * time.Second,
			Reason:          strings.TrimSpace(body.Reason),
		},
	)
	if err != nil {
		writeDeliveryError(w, err)
		return
	}
	if replay.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("ETag", quotedVersion(value.Version))
	writeJSON(w, http.StatusCreated, value)
}

func (a *api) getDeliveryApproval(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := a.grant(w, r, httpauth.DeliveryRead)
	if !ok {
		return
	}
	value, err := a.delivery.Queries().GetApproval(
		r.Context(),
		deliverydomain.TenantID(principal.TenantID()),
		deliverydomain.ApprovalID(r.PathValue("id")),
	)
	if err != nil {
		writeDeliveryError(w, err)
		return
	}
	w.Header().Set("ETag", quotedVersion(value.Version))
	writeJSON(w, http.StatusOK, value)
}

func (a *api) decideDeliveryApproval(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := a.grant(w, r, httpauth.DeliveryApprove)
	if !ok {
		return
	}
	if err := requireJSONContentType(r.Header.Get("Content-Type")); err != nil {
		writeProblem(w, http.StatusUnsupportedMediaType, "invalid_content_type", err.Error())
		return
	}
	key, err := deliveryIdempotencyKey(r)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_idempotency_key", err.Error())
		return
	}
	expected, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		writeProblem(w, http.StatusPreconditionRequired, "if_match_required", err.Error())
		return
	}
	var body struct {
		Decision       deliveryservice.ApprovalDecision `json:"decision"`
		RejectReason   string                           `json:"reject_reason,omitempty"`
		OverrideReason string                           `json:"override_reason,omitempty"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	body.RejectReason = strings.TrimSpace(body.RejectReason)
	body.OverrideReason = strings.TrimSpace(body.OverrideReason)
	if body.OverrideReason != "" {
		if _, err := a.access.Grant(principal, httpauth.DeliveryOverride); err != nil {
			a.writeAccessError(w, err)
			return
		}
	}
	value, replay, err := a.delivery.Commands().DecideApproval(
		r.Context(),
		deliveryservice.DecideApproval{
			TenantID:        deliverydomain.TenantID(principal.TenantID()),
			Actor:           deliveryservice.Actor{Subject: principal.Subject()},
			IdempotencyKey:  key,
			ApprovalID:      deliverydomain.ApprovalID(r.PathValue("id")),
			ExpectedVersion: expected,
			Decision:        body.Decision,
			RejectReason:    body.RejectReason,
			OverrideReason:  body.OverrideReason,
		},
	)
	if err != nil {
		writeDeliveryError(w, err)
		return
	}
	if replay.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	if body.Decision == deliveryservice.RejectApproval {
		approval, getErr := a.delivery.Queries().GetApproval(
			r.Context(),
			deliverydomain.TenantID(principal.TenantID()),
			deliverydomain.ApprovalID(r.PathValue("id")),
		)
		if getErr != nil {
			writeDeliveryError(w, getErr)
			return
		}
		w.Header().Set("ETag", quotedVersion(approval.Version))
		writeJSON(w, http.StatusOK, approval)
		return
	}
	w.Header().Set("ETag", quotedVersion(value.Version))
	writeJSON(w, http.StatusAccepted, value)
}

func (a *api) getDeliveryExecution(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := a.grant(w, r, httpauth.DeliveryRead)
	if !ok {
		return
	}
	value, effects, err := a.delivery.Queries().GetExecution(
		r.Context(),
		deliverydomain.TenantID(principal.TenantID()),
		deliverydomain.ExecutionID(r.PathValue("id")),
	)
	if err != nil {
		writeDeliveryError(w, err)
		return
	}
	w.Header().Set("ETag", quotedVersion(value.Version))
	writeJSON(w, http.StatusOK, struct {
		Execution deliverydomain.DispatchExecution `json:"execution"`
		Effects   []deliverydomain.EffectRecord    `json:"effects"`
	}{
		Execution: value,
		Effects:   effects,
	})
}

func (a *api) getDeliveryArtifact(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := a.grant(w, r, httpauth.DeliveryArtifactRead)
	if !ok {
		return
	}
	reader, metadata, err := a.delivery.Queries().OpenArtifact(
		r.Context(),
		deliverydomain.TenantID(principal.TenantID()),
		deliverydomain.ArtifactDigest(r.PathValue("digest")),
	)
	if err != nil {
		writeDeliveryError(w, err)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("ETag", `"`+string(metadata.Digest)+`"`)
	w.Header().Set("X-Artifact-Kind", string(metadata.Kind))
	w.Header().Set("X-Artifact-Schema", metadata.SchemaVersion)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, reader)
}

func (a *api) deliveryProblemEvents(w http.ResponseWriter, r *http.Request) {
	a.deliveryEvents(w, r, deliveryservice.AggregateProblem)
}

func (a *api) deliveryRunEvents(w http.ResponseWriter, r *http.Request) {
	a.deliveryEvents(w, r, deliveryservice.AggregateRun)
}

func (a *api) deliveryPlanEvents(w http.ResponseWriter, r *http.Request) {
	a.deliveryEvents(w, r, deliveryservice.AggregatePlan)
}

func (a *api) deliveryRevisionEvents(w http.ResponseWriter, r *http.Request) {
	a.deliveryEvents(w, r, deliveryservice.AggregateRevision)
}

func (a *api) deliveryExecutionEvents(w http.ResponseWriter, r *http.Request) {
	a.deliveryEvents(w, r, deliveryservice.AggregateExecution)
}

func (a *api) deliveryEvents(
	w http.ResponseWriter,
	r *http.Request,
	aggregateType deliveryservice.AggregateType,
) {
	principal, _, ok := a.grant(w, r, httpauth.DeliveryAuditRead)
	if !ok {
		return
	}
	if !a.acquireSSESlot() {
		writeProblem(w, http.StatusTooManyRequests, "too_many_streams", "too many active event streams")
		return
	}
	defer a.releaseSSESlot()
	after, err := parseDeliveryCursor(r)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_cursor", err.Error())
		return
	}
	subscription, err := a.delivery.EventStreams().Subscribe(
		r.Context(),
		deliveryservice.StreamCursor{
			TenantID:      deliverydomain.TenantID(principal.TenantID()),
			AggregateType: aggregateType,
			AggregateID:   r.PathValue("id"),
			After:         after,
		},
	)
	if err != nil {
		writeDeliveryError(w, err)
		return
	}
	defer subscription.Close()
	if _, ok := w.(http.Flusher); !ok {
		writeProblem(w, http.StatusInternalServerError, "stream_unsupported", "response writer cannot stream")
		return
	}
	controller := http.NewResponseController(w)
	if err := prepareSSEWrite(r.Context(), controller); err != nil {
		if r.Context().Err() == nil {
			writeProblem(w, http.StatusInternalServerError, "stream_unsupported", "response writer cannot set deadlines")
		}
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
			if _, err := fmt.Fprintf(
				w,
				"id: %d\nevent: %s\ndata: %s\n\n",
				event.Seq,
				event.Type,
				raw,
			); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		}
	}
}

func deliveryIdempotencyKey(r *http.Request) (deliveryservice.IdempotencyKey, error) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 {
		return "", fmt.Errorf("Idempotency-Key must be provided exactly once")
	}
	value := strings.TrimSpace(values[0])
	if value == "" || value != values[0] || len(value) > 200 {
		return "", fmt.Errorf("Idempotency-Key must contain 1 to 200 non-padded characters")
	}
	return deliveryservice.IdempotencyKey(value), nil
}

func parseIfMatch(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, fmt.Errorf("If-Match must contain one quoted positive version")
	}
	version, err := strconv.ParseUint(value[1:len(value)-1], 10, 64)
	if err != nil || version == 0 {
		return 0, fmt.Errorf("If-Match must contain one quoted positive version")
	}
	return version, nil
}

func quotedVersion(value uint64) string {
	return `"` + strconv.FormatUint(value, 10) + `"`
}

func requireUintQuery(r *http.Request, name string) (uint64, error) {
	values, ok := r.URL.Query()[name]
	if len(r.URL.Query()) != 1 || !ok || len(values) != 1 {
		return 0, fmt.Errorf("%s must be provided exactly once", name)
	}
	return strconv.ParseUint(values[0], 10, 64)
}

func parseDeliveryCursor(r *http.Request) (uint64, error) {
	value := r.Header.Get("Last-Event-ID")
	if value == "" {
		values, ok := r.URL.Query()["after"]
		if !ok {
			return 0, nil
		}
		if len(values) != 1 {
			return 0, fmt.Errorf("after must be one unsigned integer")
		}
		value = values[0]
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("cursor must be an unsigned integer")
	}
	return parsed, nil
}

func writeDeliveryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, deliveryservice.ErrNotFound),
		errors.Is(err, deliveryservice.ErrArtifactUnreachable),
		errors.Is(err, deliveryartifact.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "delivery resource was not found")
	case errors.Is(err, deliveryservice.ErrIdempotencyConflict):
		writeProblem(w, http.StatusConflict, "idempotency_conflict", err.Error())
	case errors.Is(err, deliveryservice.ErrConflict),
		errors.Is(err, deliveryservice.ErrCursorAhead),
		errors.Is(err, deliveryservice.ErrApprovalExpired),
		errors.Is(err, deliveryservice.ErrApprovalStale):
		writeProblem(w, http.StatusConflict, "version_conflict", err.Error())
	case errors.Is(err, deliveryservice.ErrSeparationOfDuties):
		writeProblem(w, http.StatusForbidden, "separation_of_duties", err.Error())
	case errors.Is(err, deliveryservice.ErrServiceClosed):
		writeProblem(w, http.StatusServiceUnavailable, "service_closing", "service is shutting down")
	case errors.Is(err, deliveryartifact.ErrIntegrity):
		writeProblem(w, http.StatusConflict, "artifact_integrity", "artifact integrity verification failed")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeProblem(w, http.StatusRequestTimeout, "request_cancelled", "request was cancelled")
	default:
		writeProblem(w, http.StatusInternalServerError, "internal_error", "request failed")
	}
}
