CREATE TABLE waybill.inbox_events (
    tenant_id text NOT NULL,
    source text NOT NULL,
    event_id text NOT NULL,
    event_type text NOT NULL,
    subject text,
    event_time timestamptz,
    received_at timestamptz NOT NULL DEFAULT now(),
    payload jsonb NOT NULL,
    payload_hash text NOT NULL,
    status text NOT NULL DEFAULT 'received',
    incident_id text,
    processed_at timestamptz,
    error_code text,
    CONSTRAINT inbox_events_pk PRIMARY KEY (tenant_id, source, event_id),
    CONSTRAINT inbox_events_payload_object_ck CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT inbox_events_payload_hash_ck CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT inbox_events_status_ck CHECK (status IN ('received', 'processed', 'rejected')),
    CONSTRAINT inbox_events_result_ck CHECK (
        (status = 'received' AND processed_at IS NULL AND error_code IS NULL) OR
        (status = 'processed' AND processed_at IS NOT NULL AND incident_id IS NOT NULL AND error_code IS NULL) OR
        (status = 'rejected' AND processed_at IS NOT NULL AND error_code IS NOT NULL)
    )
);

CREATE TABLE waybill.incidents (
    tenant_id text NOT NULL,
    incident_id text NOT NULL,
    source text NOT NULL,
    source_incident_key text NOT NULL,
    waybill_id text NOT NULL,
    kind text NOT NULL,
    status text NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    latest_event_time timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT incidents_pk PRIMARY KEY (tenant_id, incident_id),
    CONSTRAINT incidents_source_key_uq UNIQUE (tenant_id, source, source_incident_key),
    CONSTRAINT incidents_version_ck CHECK (version > 0),
    CONSTRAINT incidents_status_ck CHECK (status IN ('open', 'investigating', 'awaiting_approval', 'executing', 'resolved', 'manual_review', 'failed'))
);

CREATE INDEX incidents_status_idx
    ON waybill.incidents (tenant_id, status, updated_at DESC);

CREATE TABLE waybill.runs (
    tenant_id text NOT NULL,
    run_id text NOT NULL,
    incident_id text NOT NULL,
    waybill_id text NOT NULL,
    status text NOT NULL,
    plan_version integer NOT NULL DEFAULT 1,
    sdk_run_id text,
    checkpoint_version bigint NOT NULL DEFAULT 0,
    last_audit_seq bigint NOT NULL DEFAULT 0,
    last_audit_hash text,
    lease_owner text,
    lease_deadline timestamptz,
    fencing_token bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    closed_at timestamptz,
    CONSTRAINT runs_pk PRIMARY KEY (tenant_id, run_id),
    CONSTRAINT runs_incident_fk FOREIGN KEY (tenant_id, incident_id)
        REFERENCES waybill.incidents (tenant_id, incident_id),
    CONSTRAINT runs_plan_version_ck CHECK (plan_version > 0),
    CONSTRAINT runs_checkpoint_version_ck CHECK (checkpoint_version >= 0),
    CONSTRAINT runs_last_audit_seq_ck CHECK (last_audit_seq >= 0),
    CONSTRAINT runs_last_audit_hash_ck CHECK (last_audit_hash IS NULL OR last_audit_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT runs_fencing_token_ck CHECK (fencing_token >= 0),
    CONSTRAINT runs_lease_ck CHECK ((lease_owner IS NULL) = (lease_deadline IS NULL)),
    CONSTRAINT runs_status_ck CHECK (status IN ('started', 'investigating', 'awaiting_approval', 'executing', 'completed', 'rejected', 'failed', 'manual_review')),
    CONSTRAINT runs_closed_ck CHECK (
        (status IN ('completed', 'rejected', 'failed', 'manual_review') AND closed_at IS NOT NULL) OR
        (status NOT IN ('completed', 'rejected', 'failed', 'manual_review') AND closed_at IS NULL)
    )
);

CREATE UNIQUE INDEX runs_sdk_run_uq
    ON waybill.runs (tenant_id, sdk_run_id)
    WHERE sdk_run_id IS NOT NULL;

CREATE INDEX runs_claim_idx
    ON waybill.runs (tenant_id, status, lease_deadline, created_at)
    WHERE status IN ('started', 'investigating', 'executing');

CREATE TABLE waybill.evidence_snapshots (
    tenant_id text NOT NULL,
    evidence_id text NOT NULL,
    run_id text NOT NULL,
    kind text NOT NULL,
    source text NOT NULL,
    collected_at timestamptz NOT NULL,
    payload jsonb NOT NULL,
    payload_hash text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT evidence_snapshots_pk PRIMARY KEY (tenant_id, evidence_id),
    CONSTRAINT evidence_snapshots_run_fk FOREIGN KEY (tenant_id, run_id)
        REFERENCES waybill.runs (tenant_id, run_id),
    CONSTRAINT evidence_snapshots_payload_object_ck CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT evidence_snapshots_payload_hash_ck CHECK (payload_hash ~ '^[0-9a-f]{64}$')
);

CREATE INDEX evidence_snapshots_run_idx
    ON waybill.evidence_snapshots (tenant_id, run_id, collected_at);

CREATE TABLE waybill.proposals (
    tenant_id text NOT NULL,
    proposal_id text NOT NULL,
    run_id text NOT NULL,
    proposal_version integer NOT NULL,
    status text NOT NULL DEFAULT 'proposed',
    items jsonb NOT NULL,
    arguments_hash text NOT NULL,
    model_version text NOT NULL,
    prompt_version text NOT NULL,
    tool_contract_version text NOT NULL,
    policy_version text NOT NULL,
    reason text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT proposals_pk PRIMARY KEY (tenant_id, proposal_id),
    CONSTRAINT proposals_run_fk FOREIGN KEY (tenant_id, run_id)
        REFERENCES waybill.runs (tenant_id, run_id),
    CONSTRAINT proposals_run_version_uq UNIQUE (tenant_id, run_id, proposal_version),
    CONSTRAINT proposals_version_ck CHECK (proposal_version > 0),
    CONSTRAINT proposals_status_ck CHECK (status IN ('proposed', 'superseded', 'approved', 'rejected')),
    CONSTRAINT proposals_items_ck CHECK (jsonb_typeof(items) = 'array' AND jsonb_array_length(items) > 0),
    CONSTRAINT proposals_arguments_hash_ck CHECK (arguments_hash ~ '^[0-9a-f]{64}$')
);

CREATE TABLE waybill.approvals (
    tenant_id text NOT NULL,
    approval_id text NOT NULL,
    incident_id text NOT NULL,
    run_id text NOT NULL,
    proposal_id text NOT NULL,
    proposal_version integer NOT NULL,
    status text NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    required_role text NOT NULL,
    required_approvals smallint NOT NULL DEFAULT 1,
    reason text NOT NULL,
    evidence_refs jsonb NOT NULL DEFAULT '[]'::jsonb,
    requested_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    decided_by text,
    decided_at timestamptz,
    reject_reason text,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT approvals_pk PRIMARY KEY (tenant_id, approval_id),
    CONSTRAINT approvals_incident_fk FOREIGN KEY (tenant_id, incident_id)
        REFERENCES waybill.incidents (tenant_id, incident_id),
    CONSTRAINT approvals_run_fk FOREIGN KEY (tenant_id, run_id)
        REFERENCES waybill.runs (tenant_id, run_id),
    CONSTRAINT approvals_proposal_fk FOREIGN KEY (tenant_id, proposal_id)
        REFERENCES waybill.proposals (tenant_id, proposal_id),
    CONSTRAINT approvals_proposal_uq UNIQUE (tenant_id, proposal_id),
    CONSTRAINT approvals_version_ck CHECK (version > 0),
    CONSTRAINT approvals_required_count_ck CHECK (required_approvals > 0),
    CONSTRAINT approvals_expiry_ck CHECK (expires_at > requested_at),
    CONSTRAINT approvals_evidence_refs_ck CHECK (jsonb_typeof(evidence_refs) = 'array'),
    CONSTRAINT approvals_status_ck CHECK (status IN ('pending', 'confirmed', 'executing', 'executed', 'partially_failed', 'reconciliation_required', 'rejected', 'expired', 'failed')),
    CONSTRAINT approvals_decision_ck CHECK (
        (status = 'pending' AND decided_by IS NULL AND decided_at IS NULL AND reject_reason IS NULL) OR
        (status = 'rejected' AND decided_by IS NOT NULL AND decided_at IS NOT NULL AND reject_reason IS NOT NULL) OR
        (status NOT IN ('pending', 'rejected') AND decided_by IS NOT NULL AND decided_at IS NOT NULL)
    )
);

CREATE INDEX approvals_pending_idx
    ON waybill.approvals (tenant_id, expires_at, approval_id)
    WHERE status = 'pending';

CREATE TABLE waybill.effects (
    tenant_id text NOT NULL,
    effect_id text NOT NULL,
    run_id text NOT NULL,
    proposal_id text NOT NULL,
    identity_version text NOT NULL,
    action text NOT NULL,
    target text NOT NULL,
    arguments jsonb NOT NULL,
    arguments_hash text NOT NULL,
    idempotency_key text NOT NULL,
    status text NOT NULL DEFAULT 'prepared',
    attempt integer NOT NULL DEFAULT 0,
    external_ref text,
    response jsonb,
    last_error_code text,
    retry_after timestamptz,
    lease_owner text,
    lease_deadline timestamptz,
    fencing_token bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT effects_pk PRIMARY KEY (tenant_id, effect_id),
    CONSTRAINT effects_run_fk FOREIGN KEY (tenant_id, run_id)
        REFERENCES waybill.runs (tenant_id, run_id),
    CONSTRAINT effects_proposal_fk FOREIGN KEY (tenant_id, proposal_id)
        REFERENCES waybill.proposals (tenant_id, proposal_id),
    CONSTRAINT effects_idempotency_key_uq UNIQUE (tenant_id, idempotency_key),
    CONSTRAINT effects_identity_version_ck CHECK (identity_version IN ('effect-v1')),
    CONSTRAINT effects_action_ck CHECK (action IN ('tms.reassign', 'tms.create_claim', 'notify.send_sms')),
    CONSTRAINT effects_arguments_object_ck CHECK (jsonb_typeof(arguments) = 'object'),
    CONSTRAINT effects_arguments_hash_ck CHECK (arguments_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT effects_status_ck CHECK (status IN ('prepared', 'dispatching', 'succeeded', 'retryable_failed', 'permanent_failed', 'unknown', 'reconciling', 'manual_review')),
    CONSTRAINT effects_attempt_ck CHECK (attempt >= 0),
    CONSTRAINT effects_fencing_token_ck CHECK (fencing_token >= 0),
    CONSTRAINT effects_lease_ck CHECK ((lease_owner IS NULL) = (lease_deadline IS NULL))
);

CREATE INDEX effects_claim_idx
    ON waybill.effects (tenant_id, status, retry_after, lease_deadline, effect_id)
    WHERE status IN ('prepared', 'retryable_failed');

CREATE INDEX effects_reconcile_idx
    ON waybill.effects (tenant_id, status, lease_deadline, effect_id)
    WHERE status IN ('unknown', 'reconciling');

CREATE TABLE waybill.approval_effects (
    tenant_id text NOT NULL,
    approval_id text NOT NULL,
    effect_id text NOT NULL,
    ordinal integer NOT NULL,
    display_arguments jsonb NOT NULL,
    arguments_hash text NOT NULL,
    CONSTRAINT approval_effects_pk PRIMARY KEY (tenant_id, approval_id, effect_id),
    CONSTRAINT approval_effects_approval_fk FOREIGN KEY (tenant_id, approval_id)
        REFERENCES waybill.approvals (tenant_id, approval_id),
    CONSTRAINT approval_effects_effect_fk FOREIGN KEY (tenant_id, effect_id)
        REFERENCES waybill.effects (tenant_id, effect_id),
    CONSTRAINT approval_effects_ordinal_uq UNIQUE (tenant_id, approval_id, ordinal),
    CONSTRAINT approval_effects_ordinal_ck CHECK (ordinal >= 0),
    CONSTRAINT approval_effects_display_object_ck CHECK (jsonb_typeof(display_arguments) = 'object'),
    CONSTRAINT approval_effects_arguments_hash_ck CHECK (arguments_hash ~ '^[0-9a-f]{64}$')
);

CREATE TABLE waybill.agent_checkpoints (
    tenant_id text NOT NULL,
    namespace text NOT NULL,
    thread_id text NOT NULL,
    sdk_run_id text NOT NULL,
    checkpoint_version bigint NOT NULL,
    previous_sdk_run_id text,
    conversation_id text NOT NULL,
    ciphertext bytea NOT NULL,
    payload_hash text NOT NULL,
    encryption_key_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agent_checkpoints_pk PRIMARY KEY (tenant_id, namespace, thread_id, checkpoint_version),
    CONSTRAINT agent_checkpoints_sdk_run_uq UNIQUE (tenant_id, namespace, thread_id, sdk_run_id),
    CONSTRAINT agent_checkpoints_version_ck CHECK (checkpoint_version > 0),
    CONSTRAINT agent_checkpoints_ciphertext_ck CHECK (octet_length(ciphertext) > 0),
    CONSTRAINT agent_checkpoints_payload_hash_ck CHECK (payload_hash ~ '^[0-9a-f]{64}$')
);

CREATE TABLE waybill.audit_events (
    tenant_id text NOT NULL,
    run_id text NOT NULL,
    seq bigint NOT NULL,
    event_id text NOT NULL,
    schema_version integer NOT NULL,
    occurred_at timestamptz NOT NULL,
    actor_type text NOT NULL,
    actor_id text,
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    prev_hash text NOT NULL,
    hash text NOT NULL,
    CONSTRAINT audit_events_pk PRIMARY KEY (tenant_id, run_id, seq),
    CONSTRAINT audit_events_run_fk FOREIGN KEY (tenant_id, run_id)
        REFERENCES waybill.runs (tenant_id, run_id),
    CONSTRAINT audit_events_event_id_uq UNIQUE (tenant_id, run_id, event_id),
    CONSTRAINT audit_events_seq_ck CHECK (seq > 0),
    CONSTRAINT audit_events_schema_version_ck CHECK (schema_version > 0),
    CONSTRAINT audit_events_actor_ck CHECK (actor_type IN ('agent', 'human', 'system')),
    CONSTRAINT audit_events_payload_object_ck CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT audit_events_prev_hash_ck CHECK (prev_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT audit_events_hash_ck CHECK (hash ~ '^[0-9a-f]{64}$')
);

CREATE TABLE waybill.outbox_events (
    tenant_id text NOT NULL,
    source text NOT NULL,
    event_id text NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id text NOT NULL,
    aggregate_version bigint NOT NULL,
    event_type text NOT NULL,
    subject text,
    payload jsonb NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    available_at timestamptz NOT NULL DEFAULT now(),
    attempt integer NOT NULL DEFAULT 0,
    lease_owner text,
    lease_deadline timestamptz,
    fencing_token bigint NOT NULL DEFAULT 0,
    published_at timestamptz,
    last_error_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT outbox_events_pk PRIMARY KEY (tenant_id, source, event_id),
    CONSTRAINT outbox_events_source_event_uq UNIQUE (source, event_id),
    CONSTRAINT outbox_events_aggregate_version_ck CHECK (aggregate_version > 0),
    CONSTRAINT outbox_events_payload_object_ck CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT outbox_events_status_ck CHECK (status IN ('pending', 'publishing', 'published', 'retryable_failed', 'permanent_failed')),
    CONSTRAINT outbox_events_attempt_ck CHECK (attempt >= 0),
    CONSTRAINT outbox_events_fencing_token_ck CHECK (fencing_token >= 0),
    CONSTRAINT outbox_events_lease_ck CHECK ((lease_owner IS NULL) = (lease_deadline IS NULL)),
    CONSTRAINT outbox_events_published_ck CHECK (
        (status = 'published' AND published_at IS NOT NULL) OR
        (status <> 'published' AND published_at IS NULL)
    )
);

CREATE INDEX outbox_claim_idx
    ON waybill.outbox_events (status, available_at, lease_deadline, created_at)
    WHERE status IN ('pending', 'retryable_failed');
