ALTER TABLE waybill.audit_events
    ADD COLUMN payload_canonical bytea;

UPDATE waybill.audit_events
SET payload_canonical = convert_to(payload::text, 'UTF8')
WHERE payload_canonical IS NULL;

ALTER TABLE waybill.audit_events
    ALTER COLUMN payload_canonical SET NOT NULL,
    ADD CONSTRAINT audit_events_payload_canonical_ck
        CHECK (octet_length(payload_canonical) > 0);

ALTER TABLE waybill.approvals
    ADD COLUMN sdk_run_id text,
    ADD COLUMN waybill_id text,
    ADD COLUMN items jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN evidence jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD CONSTRAINT approvals_items_ck CHECK (jsonb_typeof(items) = 'array'),
    ADD CONSTRAINT approvals_evidence_ck CHECK (jsonb_typeof(evidence) = 'array');

ALTER TABLE waybill.effects
    ADD COLUMN call_id text,
    ADD COLUMN wire_name text,
    ADD COLUMN response_canonical bytea,
    ADD COLUMN reconciliation_attempt integer NOT NULL DEFAULT 0,
    ADD CONSTRAINT effects_reconciliation_attempt_ck CHECK (reconciliation_attempt >= 0);

ALTER TABLE waybill.agent_checkpoints
    ADD COLUMN group_id text NOT NULL DEFAULT 'default',
    ADD COLUMN metadata jsonb,
    ADD CONSTRAINT agent_checkpoints_metadata_ck
        CHECK (metadata IS NULL OR jsonb_typeof(metadata) = 'object');

CREATE TABLE waybill.agent_summaries (
    tenant_id text NOT NULL,
    namespace text NOT NULL,
    thread_id text NOT NULL,
    summary_id text NOT NULL,
    payload bytea NOT NULL,
    payload_hash text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agent_summaries_pk PRIMARY KEY (tenant_id, namespace, thread_id),
    CONSTRAINT agent_summaries_payload_ck CHECK (octet_length(payload) > 0),
    CONSTRAINT agent_summaries_payload_hash_ck CHECK (payload_hash ~ '^[0-9a-f]{64}$')
);

CREATE TABLE waybill.run_quarantines (
    tenant_id text NOT NULL,
    run_id text NOT NULL,
    reason text NOT NULL,
    detail_code text NOT NULL,
    observed_seq bigint NOT NULL DEFAULT 0,
    observed_hash text,
    quarantined_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT run_quarantines_pk PRIMARY KEY (tenant_id, run_id),
    CONSTRAINT run_quarantines_run_fk FOREIGN KEY (tenant_id, run_id)
        REFERENCES waybill.runs (tenant_id, run_id),
    CONSTRAINT run_quarantines_seq_ck CHECK (observed_seq >= 0),
    CONSTRAINT run_quarantines_hash_ck
        CHECK (observed_hash IS NULL OR observed_hash ~ '^[0-9a-f]{64}$')
);

CREATE INDEX runs_recovery_claim_idx
    ON waybill.runs (tenant_id, lease_deadline, updated_at, run_id)
    WHERE status IN ('started', 'investigating', 'executing');

CREATE INDEX outbox_tenant_claim_idx
    ON waybill.outbox_events (tenant_id, status, available_at, lease_deadline, created_at)
    WHERE status IN ('pending', 'retryable_failed');
