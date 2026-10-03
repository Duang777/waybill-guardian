ALTER TABLE waybill.inbox_events
    DROP CONSTRAINT inbox_events_result_ck,
    ADD COLUMN profile_version text NOT NULL DEFAULT 'legacy-v0',
    ADD COLUMN data_schema text,
    ADD COLUMN record_time timestamptz,
    ADD COLUMN waybill_id text,
    ADD COLUMN source_incident_key text,
    ADD COLUMN source_version bigint,
    ADD COLUMN event_kind text,
    ADD COLUMN correction_target_id text,
    ADD COLUMN payload_canonical bytea,
    ADD COLUMN event_canonical bytea,
    ADD COLUMN event_hash text,
    ADD COLUMN result_disposition text,
    ADD COLUMN incident_version bigint,
    ADD COLUMN result_canonical bytea,
    ADD COLUMN outbox_source text,
    ADD COLUMN outbox_event_id text,
    ADD CONSTRAINT inbox_events_profile_version_ck
        CHECK (profile_version IN ('legacy-v0', 'waybill-event-v1')),
    ADD CONSTRAINT inbox_events_event_hash_ck
        CHECK (event_hash IS NULL OR event_hash ~ '^[0-9a-f]{64}$'),
    ADD CONSTRAINT inbox_events_source_version_ck
        CHECK (source_version IS NULL OR source_version > 0),
    ADD CONSTRAINT inbox_events_event_kind_ck
        CHECK (
            event_kind IS NULL
            OR event_kind IN ('delay_detected', 'delay_corrected')
        ),
    ADD CONSTRAINT inbox_events_canonical_bytes_ck
        CHECK (
            (payload_canonical IS NULL OR octet_length(payload_canonical) > 0)
            AND
            (event_canonical IS NULL OR octet_length(event_canonical) > 0)
            AND
            (result_canonical IS NULL OR octet_length(result_canonical) > 0)
        ),
    ADD CONSTRAINT inbox_events_correction_target_ck
        CHECK (
            (event_kind = 'delay_corrected' AND correction_target_id IS NOT NULL)
            OR
            (
                event_kind IS DISTINCT FROM 'delay_corrected'
                AND correction_target_id IS NULL
            )
        ),
    ADD CONSTRAINT inbox_events_outbox_pair_ck
        CHECK ((outbox_source IS NULL) = (outbox_event_id IS NULL)),
    ADD CONSTRAINT inbox_events_v1_shape_ck
        CHECK (
            profile_version <> 'waybill-event-v1'
            OR (
                data_schema IS NOT NULL
                AND event_time IS NOT NULL
                AND record_time IS NOT NULL
                AND waybill_id IS NOT NULL
                AND source_incident_key IS NOT NULL
                AND source_version IS NOT NULL
                AND event_kind IS NOT NULL
                AND payload_canonical IS NOT NULL
                AND event_canonical IS NOT NULL
                AND event_hash IS NOT NULL
            )
        ),
    ADD CONSTRAINT inbox_events_result_ck
        CHECK (
            (
                status = 'received'
                AND processed_at IS NULL
                AND incident_id IS NULL
                AND error_code IS NULL
                AND result_disposition IS NULL
                AND incident_version IS NULL
                AND result_canonical IS NULL
                AND outbox_source IS NULL
                AND outbox_event_id IS NULL
            )
            OR
            (
                status = 'processed'
                AND processed_at IS NOT NULL
                AND incident_id IS NOT NULL
                AND error_code IS NULL
                AND (
                    (
                        profile_version = 'legacy-v0'
                        AND result_disposition IS NULL
                        AND incident_version IS NULL
                        AND result_canonical IS NULL
                        AND outbox_source IS NULL
                        AND outbox_event_id IS NULL
                    )
                    OR
                    (
                        profile_version = 'waybill-event-v1'
                        AND result_disposition IS NOT NULL
                        AND incident_version > 0
                        AND result_canonical IS NOT NULL
                    )
                )
            )
            OR
            (
                status = 'rejected'
                AND processed_at IS NOT NULL
                AND error_code IS NOT NULL
            )
        );

CREATE INDEX inbox_events_incident_stream_idx
    ON waybill.inbox_events (
        tenant_id,
        source,
        source_incident_key,
        source_version,
        event_id
    )
    WHERE profile_version = 'waybill-event-v1'
      AND status IN ('received', 'processed');

CREATE INDEX inbox_events_correction_target_idx
    ON waybill.inbox_events (
        tenant_id,
        source,
        correction_target_id
    )
    WHERE correction_target_id IS NOT NULL;

ALTER TABLE waybill.incidents
    ADD COLUMN transport_state text NOT NULL DEFAULT 'none',
    ADD COLUMN transport_payload jsonb,
    ADD COLUMN transport_hash text,
    ADD COLUMN current_source_version bigint,
    ADD COLUMN current_event_source text,
    ADD COLUMN current_event_id text,
    ADD COLUMN review_code text,
    ADD COLUMN conflict_count integer NOT NULL DEFAULT 0,
    ADD COLUMN conflict_refs jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN requires_reinvestigation boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT incidents_transport_state_ck
        CHECK (
            transport_state IN (
                'none',
                'active',
                'retracted',
                'pending_correction',
                'conflicted'
            )
        ),
    ADD CONSTRAINT incidents_transport_payload_ck
        CHECK (
            transport_payload IS NULL
            OR jsonb_typeof(transport_payload) = 'object'
        ),
    ADD CONSTRAINT incidents_transport_hash_ck
        CHECK (
            transport_hash IS NULL
            OR transport_hash ~ '^[0-9a-f]{64}$'
        ),
    ADD CONSTRAINT incidents_source_version_ck
        CHECK (
            current_source_version IS NULL
            OR current_source_version > 0
        ),
    ADD CONSTRAINT incidents_current_event_pair_ck
        CHECK (
            (current_event_source IS NULL) = (current_event_id IS NULL)
        ),
    ADD CONSTRAINT incidents_conflict_count_ck
        CHECK (conflict_count >= 0),
    ADD CONSTRAINT incidents_conflict_refs_ck
        CHECK (
            jsonb_typeof(conflict_refs) = 'array'
            AND jsonb_array_length(conflict_refs) <= 16
        ),
    ADD CONSTRAINT incidents_transport_projection_ck
        CHECK (
            (
                transport_state = 'none'
                AND transport_payload IS NULL
                AND transport_hash IS NULL
                AND current_source_version IS NULL
                AND current_event_source IS NULL
                AND current_event_id IS NULL
                AND review_code IS NULL
                AND conflict_count = 0
                AND conflict_refs = '[]'::jsonb
                AND requires_reinvestigation = false
            )
            OR
            (
                transport_state = 'active'
                AND transport_payload IS NOT NULL
                AND transport_hash IS NOT NULL
                AND current_source_version IS NOT NULL
                AND current_event_source IS NOT NULL
                AND current_event_id IS NOT NULL
                AND review_code IS NULL
            )
            OR
            (
                transport_state = 'retracted'
                AND transport_payload IS NOT NULL
                AND transport_hash IS NOT NULL
                AND current_source_version IS NULL
                AND current_event_source IS NULL
                AND current_event_id IS NULL
                AND review_code IS NULL
            )
            OR
            (
                transport_state IN ('pending_correction', 'conflicted')
                AND transport_payload IS NOT NULL
                AND transport_hash IS NOT NULL
                AND review_code IS NOT NULL
            )
        );

ALTER TABLE waybill.outbox_events
    ADD COLUMN event_time timestamptz,
    ADD COLUMN data_content_type text NOT NULL DEFAULT 'application/json',
    ADD COLUMN data_schema text,
    ADD COLUMN payload_canonical bytea,
    ADD COLUMN requeue_count integer NOT NULL DEFAULT 0,
    ADD COLUMN last_requeued_at timestamptz,
    ADD COLUMN last_requeued_by text,
    ADD COLUMN last_requeue_reason text;

UPDATE waybill.outbox_events
SET event_time = created_at,
    data_schema = CASE
        WHEN aggregate_type = 'run'
        THEN 'urn:waybill-guardian:schema:run-audit:v1'
        ELSE 'urn:waybill-guardian:schema:legacy-event:v1'
    END,
    payload_canonical = convert_to(payload::text, 'UTF8')
WHERE event_time IS NULL
   OR data_schema IS NULL
   OR payload_canonical IS NULL;

ALTER TABLE waybill.outbox_events
    ALTER COLUMN event_time SET NOT NULL,
    ALTER COLUMN data_schema SET NOT NULL,
    ALTER COLUMN payload_canonical SET NOT NULL,
    ADD CONSTRAINT outbox_events_data_content_type_ck
        CHECK (data_content_type = 'application/json'),
    ADD CONSTRAINT outbox_events_payload_canonical_ck
        CHECK (octet_length(payload_canonical) > 0),
    ADD CONSTRAINT outbox_events_requeue_count_ck
        CHECK (requeue_count >= 0),
    ADD CONSTRAINT outbox_events_requeue_audit_ck
        CHECK (
            (
                requeue_count = 0
                AND last_requeued_at IS NULL
                AND last_requeued_by IS NULL
                AND last_requeue_reason IS NULL
            )
            OR
            (
                requeue_count > 0
                AND last_requeued_at IS NOT NULL
                AND last_requeued_by IS NOT NULL
                AND last_requeue_reason IS NOT NULL
            )
        ),
    ADD CONSTRAINT outbox_events_aggregate_version_uq
        UNIQUE (tenant_id, aggregate_type, aggregate_id, aggregate_version);

ALTER TABLE waybill.inbox_events
    ADD CONSTRAINT inbox_events_outbox_fk
        FOREIGN KEY (tenant_id, outbox_source, outbox_event_id)
        REFERENCES waybill.outbox_events (tenant_id, source, event_id);
