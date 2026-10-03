package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/events"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	eventProfileV1       = "waybill-event-v1"
	incidentOutboxSource = "urn:waybill-guardian:incidents"
	incidentOutboxSchema = "urn:waybill-guardian:schema:incident-snapshot:v1"
)

var (
	incidentNamespace = uuid.MustParse("927e8594-9e0d-5d67-a3d0-19085d3fe266")
	outboxNamespace   = uuid.MustParse("20d974aa-a7b4-5c33-84f1-77150ec5afcf")
)

type ambiguousEventCommitError struct {
	err error
}

func (e *ambiguousEventCommitError) Error() string {
	return fmt.Sprintf("commit PostgreSQL event ingestion: %v", e.err)
}

func (e *ambiguousEventCommitError) Unwrap() error {
	return e.err
}

type incidentWork struct {
	id         domain.IncidentID
	episode    events.Episode
	isNew      bool
	version    int64
	status     string
	storedHash *string
	hasRun     bool
	projection events.Projection
	changed    bool
	outboxRef  *events.EventRef
}

func (r *Repository) IngestEvent(
	ctx context.Context,
	submission events.Submission,
) (events.Result, error) {
	if err := r.checkOpen(); err != nil {
		return events.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return events.Result{}, err
	}

	for attempt := 0; attempt < 3; attempt++ {
		result, err := r.ingestEventOnce(ctx, submission)
		if err == nil {
			return result, nil
		}
		var commitErr *ambiguousEventCommitError
		if errors.As(err, &commitErr) {
			resolveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			resolved, found, resolveErr := r.resolveCommittedEvent(resolveCtx, submission)
			cancel()
			if resolveErr != nil {
				return events.Result{}, fmt.Errorf("%w: %v", events.ErrEventsUnavailable, resolveErr)
			}
			if found {
				return resolved, nil
			}
			if attempt == 0 && ctx.Err() == nil {
				continue
			}
			return events.Result{}, fmt.Errorf(
				"%w: event commit outcome is unknown",
				events.ErrEventsUnavailable,
			)
		}
		if !retryableTransactionError(err) || attempt == 2 || ctx.Err() != nil {
			return events.Result{}, err
		}
	}
	return events.Result{}, fmt.Errorf("%w: transaction retry exhausted", events.ErrEventsUnavailable)
}

func (r *Repository) ingestEventOnce(
	ctx context.Context,
	submission events.Submission,
) (events.Result, error) {
	record := submission.Record()
	tx, err := r.db.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return events.Result{}, fmt.Errorf("begin PostgreSQL event ingestion: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	inserted, err := r.insertInboxEvent(ctx, tx, submission)
	if err != nil {
		return events.Result{}, err
	}
	if !inserted {
		result, found, err := r.readStoredEventResult(ctx, tx, submission)
		if err != nil {
			return events.Result{}, err
		}
		if !found {
			return events.Result{}, fmt.Errorf("conflicting inbox row disappeared")
		}
		result.Replayed = true
		return result, nil
	}

	if err := lockEventIdentities(ctx, tx, r.tenantID, record); err != nil {
		return events.Result{}, err
	}
	primary, err := r.ensureIncident(ctx, tx, record.Episode())
	if err != nil {
		return events.Result{}, err
	}
	affected, err := r.affectedIncidents(ctx, tx, primary, record)
	if err != nil {
		return events.Result{}, err
	}
	if err := r.lockIncidents(ctx, tx, affected); err != nil {
		return events.Result{}, err
	}

	transitionTime, err := transactionTime(ctx, tx)
	if err != nil {
		return events.Result{}, err
	}
	for _, work := range affected {
		projection, err := r.projectIncident(ctx, tx, work.episode)
		if err != nil {
			return events.Result{}, err
		}
		work.projection = projection
		work.changed = work.storedHash == nil ||
			*work.storedHash != string(projection.Fingerprint)
		if !work.changed {
			continue
		}
		if !work.isNew {
			work.version++
		}
		if err := r.updateIncidentProjection(ctx, tx, work, transitionTime); err != nil {
			return events.Result{}, err
		}
		outboxRef, err := r.insertIncidentOutbox(ctx, tx, work, record.Ref.ID, transitionTime)
		if err != nil {
			return events.Result{}, err
		}
		work.outboxRef = &outboxRef
	}

	primaryWork := affected[primary.id]
	if primaryWork == nil {
		return events.Result{}, fmt.Errorf("primary incident was not projected")
	}
	var before *events.Projection
	if primaryWork.storedHash != nil {
		before = &events.Projection{Fingerprint: events.Digest(*primaryWork.storedHash)}
	}
	result := events.Result{
		EventID:         record.Ref.ID,
		IncidentID:      primaryWork.id,
		IncidentVersion: primaryWork.version,
		Disposition:     events.Classify(before, primaryWork.projection, record),
	}
	resultJSON, err := result.CanonicalJSON()
	if err != nil {
		return events.Result{}, fmt.Errorf("marshal event ingestion result: %w", err)
	}
	if err := r.completeInboxEvent(
		ctx,
		tx,
		record.Ref,
		result,
		resultJSON,
		primaryWork.outboxRef,
		transitionTime,
	); err != nil {
		return events.Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return events.Result{}, &ambiguousEventCommitError{err: err}
	}
	return result, nil
}

func (r *Repository) insertInboxEvent(
	ctx context.Context,
	tx pgx.Tx,
	submission events.Submission,
) (bool, error) {
	record := submission.Record()
	eventKind := "delay_detected"
	var correctionTargetID any
	if record.Type == events.DelayCorrectedType {
		eventKind = "delay_corrected"
		correctionTargetID = record.Correction.Corrects.ID
	}
	var inserted string
	err := tx.QueryRow(ctx, `
		INSERT INTO waybill.inbox_events (
			tenant_id, source, event_id, event_type, subject, event_time,
			payload, payload_hash, profile_version, data_schema, record_time,
			waybill_id, source_incident_key, source_version, event_kind,
			correction_target_id, payload_canonical, event_canonical, event_hash
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7::jsonb, $8, $9, $10, $11,
			$12, $13, $14, $15,
			$16, $17, $18, $19
		)
		ON CONFLICT (tenant_id, source, event_id) DO NOTHING
		RETURNING event_id
	`, r.tenantID, record.Ref.Source, record.Ref.ID, record.Type, record.Subject,
		record.EnvelopeTime, submission.CanonicalEvent(), submission.DataHash(),
		eventProfileV1, record.DataSchema, record.RecordTime, record.WaybillID,
		record.IncidentKey, record.SourceVersion, eventKind, correctionTargetID,
		submission.CanonicalData(), submission.CanonicalEvent(), submission.EventHash(),
	).Scan(&inserted)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	default:
		return false, fmt.Errorf("insert PostgreSQL inbox event: %w", MapError(err))
	}
}

func (r *Repository) readStoredEventResult(
	ctx context.Context,
	query interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	submission events.Submission,
) (events.Result, bool, error) {
	var profile, status string
	var eventHash *string
	var resultJSON []byte
	err := query.QueryRow(ctx, `
		SELECT profile_version, event_hash, status, result_canonical
		FROM waybill.inbox_events
		WHERE tenant_id = $1 AND source = $2 AND event_id = $3
	`, r.tenantID, submission.Source(), submission.ID()).Scan(
		&profile,
		&eventHash,
		&status,
		&resultJSON,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return events.Result{}, false, nil
	}
	if err != nil {
		return events.Result{}, false, fmt.Errorf("read PostgreSQL inbox result: %w", err)
	}
	if profile != eventProfileV1 || eventHash == nil {
		return events.Result{}, true, events.ErrLegacyEventIdentity
	}
	if *eventHash != string(submission.EventHash()) {
		return events.Result{}, true, events.ErrEventIdentityConflict
	}
	if status != "processed" || len(resultJSON) == 0 {
		return events.Result{}, true, fmt.Errorf(
			"event inbox row %s/%s is not terminal",
			submission.Source(),
			submission.ID(),
		)
	}
	result, err := events.RestoreResult(resultJSON)
	if err != nil {
		return events.Result{}, true, err
	}
	return result, true, nil
}

func (r *Repository) resolveCommittedEvent(
	ctx context.Context,
	submission events.Submission,
) (events.Result, bool, error) {
	result, found, err := r.readStoredEventResult(ctx, r.db.pool, submission)
	if err != nil {
		return events.Result{}, found, err
	}
	return result, found, nil
}

func lockEventIdentities(
	ctx context.Context,
	tx pgx.Tx,
	tenantID string,
	record events.Record,
) error {
	keys := []int64{advisoryKey(tenantID, record.Ref.Source, record.Ref.ID)}
	if record.Correction != nil {
		keys = append(keys, advisoryKey(
			tenantID,
			record.Correction.Corrects.Source,
			record.Correction.Corrects.ID,
		))
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for index, key := range keys {
		if index > 0 && key == keys[index-1] {
			continue
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
			return fmt.Errorf("lock PostgreSQL event identity: %w", err)
		}
	}
	return nil
}

func advisoryKey(parts ...string) int64 {
	hasher := sha256.New()
	for index, part := range parts {
		if index != 0 {
			_, _ = hasher.Write([]byte{0})
		}
		_, _ = hasher.Write([]byte(part))
	}
	return int64(binary.BigEndian.Uint64(hasher.Sum(nil)[:8]))
}

func (r *Repository) ensureIncident(
	ctx context.Context,
	tx pgx.Tx,
	episode events.Episode,
) (*incidentWork, error) {
	expectedID := incidentID(r.tenantID, episode.Source, episode.IncidentKey)
	tag, err := tx.Exec(ctx, `
		INSERT INTO waybill.incidents (
			tenant_id, incident_id, source, source_incident_key,
			waybill_id, kind, status
		) VALUES ($1, $2, $3, $4, $5, 'delay', 'open')
		ON CONFLICT (tenant_id, source, source_incident_key) DO NOTHING
	`, r.tenantID, expectedID, episode.Source, episode.IncidentKey, episode.WaybillID)
	if err != nil {
		return nil, fmt.Errorf("insert PostgreSQL event incident: %w", MapError(err))
	}
	var (
		actualID        domain.IncidentID
		actualWaybillID domain.WaybillID
	)
	if err := tx.QueryRow(ctx, `
		SELECT incident_id, waybill_id
		FROM waybill.incidents
		WHERE tenant_id = $1 AND source = $2 AND source_incident_key = $3
	`, r.tenantID, episode.Source, episode.IncidentKey).Scan(
		&actualID,
		&actualWaybillID,
	); err != nil {
		return nil, fmt.Errorf("read PostgreSQL event incident: %w", err)
	}
	if actualWaybillID != episode.WaybillID {
		return nil, events.ErrIncidentIdentityConflict
	}
	return &incidentWork{
		id:      actualID,
		episode: episode,
		isNew:   tag.RowsAffected() == 1,
	}, nil
}

func (r *Repository) affectedIncidents(
	ctx context.Context,
	tx pgx.Tx,
	primary *incidentWork,
	incoming events.Record,
) (map[domain.IncidentID]*incidentWork, error) {
	affected := map[domain.IncidentID]*incidentWork{primary.id: primary}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT incident.incident_id, incident.source,
		       incident.waybill_id, incident.source_incident_key
		FROM waybill.inbox_events correction
		JOIN waybill.incidents incident
		  ON incident.tenant_id = correction.tenant_id
		 AND incident.source = correction.source
		 AND incident.source_incident_key = correction.source_incident_key
		WHERE correction.tenant_id = $1
		  AND correction.source = $2
		  AND correction.correction_target_id = $3
		  AND correction.profile_version = 'waybill-event-v1'
		  AND correction.status IN ('received', 'processed')
	`, r.tenantID, incoming.Ref.Source, incoming.Ref.ID)
	if err != nil {
		return nil, fmt.Errorf("find PostgreSQL correction dependents: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var work incidentWork
		if err := rows.Scan(
			&work.id,
			&work.episode.Source,
			&work.episode.WaybillID,
			&work.episode.IncidentKey,
		); err != nil {
			return nil, fmt.Errorf("scan PostgreSQL correction dependent: %w", err)
		}
		if existing := affected[work.id]; existing == nil {
			affected[work.id] = &work
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read PostgreSQL correction dependents: %w", err)
	}
	return affected, nil
}

func (r *Repository) lockIncidents(
	ctx context.Context,
	tx pgx.Tx,
	affected map[domain.IncidentID]*incidentWork,
) error {
	ids := make([]domain.IncidentID, 0, len(affected))
	for id := range affected {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		work := affected[id]
		var storedHash *string
		if err := tx.QueryRow(ctx, `
			SELECT version, status, transport_hash,
			       source, waybill_id, source_incident_key,
			       EXISTS (
			           SELECT 1
			           FROM waybill.runs
			           WHERE tenant_id = incident.tenant_id
			             AND incident_id = incident.incident_id
			       )
			FROM waybill.incidents incident
			WHERE tenant_id = $1 AND incident_id = $2
			FOR UPDATE
		`, r.tenantID, id).Scan(
			&work.version,
			&work.status,
			&storedHash,
			&work.episode.Source,
			&work.episode.WaybillID,
			&work.episode.IncidentKey,
			&work.hasRun,
		); err != nil {
			return fmt.Errorf("lock PostgreSQL incident %q: %w", id, err)
		}
		work.storedHash = storedHash
	}
	return nil
}

func (r *Repository) projectIncident(
	ctx context.Context,
	tx pgx.Tx,
	episode events.Episode,
) (events.Projection, error) {
	rows, err := tx.Query(ctx, `
		SELECT event_canonical
		FROM waybill.inbox_events
		WHERE tenant_id = $1
		  AND source = $2
		  AND source_incident_key = $3
		  AND profile_version = 'waybill-event-v1'
		  AND status IN ('received', 'processed')
		ORDER BY source_version, event_id
	`, r.tenantID, episode.Source, episode.IncidentKey)
	if err != nil {
		return events.Projection{}, fmt.Errorf("read PostgreSQL incident event stream: %w", err)
	}
	var records []events.Record
	for rows.Next() {
		var canonical []byte
		if err := rows.Scan(&canonical); err != nil {
			rows.Close()
			return events.Projection{}, fmt.Errorf("scan PostgreSQL incident event: %w", err)
		}
		record, err := events.RestoreCanonicalEvent(canonical)
		if err != nil {
			rows.Close()
			return events.Projection{}, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return events.Projection{}, fmt.Errorf("read PostgreSQL incident event stream: %w", err)
	}
	rows.Close()

	targetIDs := make([]string, 0)
	seenTargets := make(map[string]struct{})
	for _, record := range records {
		if record.Correction == nil {
			continue
		}
		targetID := record.Correction.Corrects.ID
		if _, exists := seenTargets[targetID]; exists {
			continue
		}
		seenTargets[targetID] = struct{}{}
		targetIDs = append(targetIDs, targetID)
	}
	sort.Strings(targetIDs)
	targets := make(map[events.EventRef]events.Record, len(targetIDs))
	if len(targetIDs) != 0 {
		targetRows, err := tx.Query(ctx, `
			SELECT event_canonical
			FROM waybill.inbox_events
			WHERE tenant_id = $1
			  AND source = $2
			  AND event_id = ANY($3::text[])
			  AND profile_version = 'waybill-event-v1'
			  AND status IN ('received', 'processed')
			ORDER BY event_id
		`, r.tenantID, episode.Source, targetIDs)
		if err != nil {
			return events.Projection{}, fmt.Errorf(
				"read PostgreSQL correction targets: %w",
				err,
			)
		}
		defer targetRows.Close()
		for targetRows.Next() {
			var canonical []byte
			if err := targetRows.Scan(&canonical); err != nil {
				return events.Projection{}, fmt.Errorf(
					"scan PostgreSQL correction target: %w",
					err,
				)
			}
			target, err := events.RestoreCanonicalEvent(canonical)
			if err != nil {
				return events.Projection{}, err
			}
			targets[target.Ref] = target
		}
		if err := targetRows.Err(); err != nil {
			return events.Projection{}, fmt.Errorf(
				"read PostgreSQL correction targets: %w",
				err,
			)
		}
	}
	return events.Reduce(events.EventSet{
		Episode:           episode,
		Records:           records,
		CorrectionTargets: targets,
	}), nil
}

func (r *Repository) updateIncidentProjection(
	ctx context.Context,
	tx pgx.Tx,
	work *incidentWork,
	transitionTime time.Time,
) error {
	projectionJSON := work.projection.CanonicalJSON()
	conflictJSON, err := json.Marshal(work.projection.ConflictRefs)
	if err != nil {
		return fmt.Errorf("marshal incident conflict references: %w", err)
	}
	var currentVersion any
	var currentSource any
	var currentID any
	var latestEventTime any
	if work.projection.CurrentRef != nil {
		currentVersion = work.projection.CurrentSourceVersion
		currentSource = work.projection.CurrentRef.Source
		currentID = work.projection.CurrentRef.ID
	}
	if work.projection.Current != nil {
		latestEventTime = work.projection.Current.EventTime
	}
	var reviewCode any
	if work.projection.ReviewCode != events.ReviewNone {
		reviewCode = work.projection.ReviewCode
	}
	tag, err := tx.Exec(ctx, `
		UPDATE waybill.incidents
		SET transport_state = $3,
		    transport_payload = $4::jsonb,
		    transport_hash = $5,
		    current_source_version = $6,
		    current_event_source = $7,
		    current_event_id = $8,
		    review_code = $9,
		    conflict_count = $10,
		    conflict_refs = $11::jsonb,
		    requires_reinvestigation = requires_reinvestigation OR $12,
		    latest_event_time = $13,
		    version = $14,
		    updated_at = $15
		WHERE tenant_id = $1 AND incident_id = $2
	`, r.tenantID, work.id, work.projection.State, projectionJSON,
		work.projection.Fingerprint, currentVersion, currentSource, currentID,
		reviewCode, work.projection.ConflictCount, conflictJSON,
		work.hasRun, latestEventTime, work.version, transitionTime)
	if err != nil {
		return fmt.Errorf("update PostgreSQL incident projection: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("incident %q disappeared while projecting", work.id)
	}
	return nil
}

func (r *Repository) insertIncidentOutbox(
	ctx context.Context,
	tx pgx.Tx,
	work *incidentWork,
	triggerEventID string,
	eventTime time.Time,
) (events.EventRef, error) {
	eventID := incidentOutboxID(r.tenantID, work.id, work.version)
	eventType := incidentEventType(work)
	payload, err := json.Marshal(struct {
		IncidentID              domain.IncidentID     `json:"incident_id"`
		IncidentVersion         int64                 `json:"incident_version"`
		WaybillID               domain.WaybillID      `json:"waybill_id"`
		Kind                    string                `json:"kind"`
		Source                  string                `json:"source"`
		SourceIncidentKey       string                `json:"source_incident_key"`
		WorkflowStatus          string                `json:"workflow_status"`
		TransportState          events.TransportState `json:"transport_state"`
		CurrentSourceVersion    events.SourceVersion  `json:"current_source_version,omitempty"`
		CurrentEvent            *events.EventRef      `json:"current_event,omitempty"`
		CurrentDelay            *events.DelayDetails  `json:"current_delay,omitempty"`
		ReviewCode              events.ReviewCode     `json:"review_code,omitempty"`
		ConflictCount           int                   `json:"conflict_count,omitempty"`
		ConflictRefs            []events.EventRef     `json:"conflict_refs,omitempty"`
		RequiresReinvestigation bool                  `json:"requires_reinvestigation"`
		TriggerEventID          string                `json:"trigger_event_id"`
	}{
		IncidentID:              work.id,
		IncidentVersion:         work.version,
		WaybillID:               work.episode.WaybillID,
		Kind:                    "delay",
		Source:                  work.episode.Source,
		SourceIncidentKey:       work.episode.IncidentKey,
		WorkflowStatus:          work.status,
		TransportState:          work.projection.State,
		CurrentSourceVersion:    work.projection.CurrentSourceVersion,
		CurrentEvent:            work.projection.CurrentRef,
		CurrentDelay:            work.projection.Current,
		ReviewCode:              work.projection.ReviewCode,
		ConflictCount:           work.projection.ConflictCount,
		ConflictRefs:            work.projection.ConflictRefs,
		RequiresReinvestigation: work.hasRun,
		TriggerEventID:          triggerEventID,
	})
	if err != nil {
		return events.EventRef{}, fmt.Errorf("marshal incident outbox payload: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO waybill.outbox_events (
			tenant_id, source, event_id, aggregate_type, aggregate_id,
			aggregate_version, event_type, subject, payload, event_time,
			data_content_type, data_schema, payload_canonical
		) VALUES (
			$1, $2, $3, 'incident', $4,
			$5, $6, $7, $8::jsonb, $9,
			'application/json', $10, $11
		)
	`, r.tenantID, incidentOutboxSource, eventID, work.id, work.version,
		eventType, "incident/"+string(work.id), payload, eventTime,
		incidentOutboxSchema, payload); err != nil {
		return events.EventRef{}, fmt.Errorf(
			"insert PostgreSQL incident outbox event: %w",
			MapError(err),
		)
	}
	return events.EventRef{Source: incidentOutboxSource, ID: eventID}, nil
}

func incidentEventType(work *incidentWork) string {
	switch work.projection.State {
	case events.TransportRetracted:
		return "com.waybill.incident.retracted.v1"
	case events.TransportPendingCorrection, events.TransportConflicted:
		return "com.waybill.incident.manual-review-required.v1"
	case events.TransportActive:
		if work.storedHash == nil {
			return "com.waybill.incident.detected.v1"
		}
		return "com.waybill.incident.corrected.v1"
	default:
		return "com.waybill.incident.manual-review-required.v1"
	}
}

func (r *Repository) completeInboxEvent(
	ctx context.Context,
	tx pgx.Tx,
	ref events.EventRef,
	result events.Result,
	resultJSON []byte,
	outboxRef *events.EventRef,
	processedAt time.Time,
) error {
	var outboxSource any
	var outboxEventID any
	if outboxRef != nil {
		outboxSource = outboxRef.Source
		outboxEventID = outboxRef.ID
	}
	tag, err := tx.Exec(ctx, `
		UPDATE waybill.inbox_events
		SET status = 'processed',
		    incident_id = $4,
		    processed_at = $5,
		    result_disposition = $6,
		    incident_version = $7,
		    result_canonical = $8,
		    outbox_source = $9,
		    outbox_event_id = $10
		WHERE tenant_id = $1 AND source = $2 AND event_id = $3
		  AND status = 'received'
	`, r.tenantID, ref.Source, ref.ID, result.IncidentID, processedAt,
		result.Disposition, result.IncidentVersion, resultJSON,
		outboxSource, outboxEventID)
	if err != nil {
		return fmt.Errorf("complete PostgreSQL inbox event: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("new inbox event was not in received state")
	}
	return nil
}

func transactionTime(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var value time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&value); err != nil {
		return time.Time{}, fmt.Errorf("read PostgreSQL transaction time: %w", err)
	}
	return value.UTC(), nil
}

func incidentID(tenantID, source, incidentKey string) domain.IncidentID {
	name := tenantID + "\x00" + source + "\x00" + incidentKey
	return domain.IncidentID(uuid.NewSHA1(incidentNamespace, []byte(name)).String())
}

func incidentOutboxID(tenantID string, id domain.IncidentID, version int64) string {
	name := fmt.Sprintf("%s\x00%s\x00%d", tenantID, id, version)
	return uuid.NewSHA1(outboxNamespace, []byte(name)).String()
}

func retryableTransactionError(err error) bool {
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return false
	}
	return postgresError.Code == "40001" || postgresError.Code == "40P01"
}

var _ events.Ingestor = (*Repository)(nil)
