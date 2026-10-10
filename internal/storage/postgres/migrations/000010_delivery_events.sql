CREATE TABLE waybill.delivery_event_heads (
    tenant_id text NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id text NOT NULL,
    last_seq bigint NOT NULL DEFAULT 0,
    last_hash text NOT NULL DEFAULT repeat('0', 64),
    updated_at timestamptz NOT NULL,
    CONSTRAINT delivery_event_heads_pk
        PRIMARY KEY (tenant_id, aggregate_type, aggregate_id),
    CONSTRAINT delivery_event_heads_type_ck CHECK (
        aggregate_type IN ('problem', 'run', 'plan', 'revision', 'execution')
    ),
    CONSTRAINT delivery_event_heads_seq_ck CHECK (last_seq >= 0),
    CONSTRAINT delivery_event_heads_hash_ck CHECK (last_hash ~ '^[0-9a-f]{64}$')
);

CREATE TABLE waybill.delivery_events (
    tenant_id text NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id text NOT NULL,
    seq bigint NOT NULL,
    event_id text NOT NULL,
    event_type text NOT NULL,
    actor text NOT NULL,
    occurred_at timestamptz NOT NULL,
    payload jsonb NOT NULL,
    payload_canonical bytea NOT NULL,
    prev_hash text NOT NULL,
    hash text NOT NULL,
    CONSTRAINT delivery_events_pk
        PRIMARY KEY (tenant_id, aggregate_type, aggregate_id, seq),
    CONSTRAINT delivery_events_head_fk
        FOREIGN KEY (tenant_id, aggregate_type, aggregate_id)
        REFERENCES waybill.delivery_event_heads (
            tenant_id,
            aggregate_type,
            aggregate_id
        ),
    CONSTRAINT delivery_events_id_uq UNIQUE (tenant_id, event_id),
    CONSTRAINT delivery_events_type_ck CHECK (
        aggregate_type IN ('problem', 'run', 'plan', 'revision', 'execution')
    ),
    CONSTRAINT delivery_events_seq_ck CHECK (seq > 0),
    CONSTRAINT delivery_events_payload_ck CHECK (
        jsonb_typeof(payload) = 'object'
        AND octet_length(payload_canonical) > 0
    ),
    CONSTRAINT delivery_events_hash_ck CHECK (
        prev_hash ~ '^[0-9a-f]{64}$'
        AND hash ~ '^[0-9a-f]{64}$'
    )
);

CREATE TABLE waybill.delivery_outbox (
    tenant_id text NOT NULL,
    event_id text NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id text NOT NULL,
    aggregate_seq bigint NOT NULL,
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    payload_canonical bytea NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    available_at timestamptz NOT NULL,
    attempt integer NOT NULL DEFAULT 0,
    lease_owner text,
    lease_deadline timestamptz,
    fencing_token bigint NOT NULL DEFAULT 0,
    published_at timestamptz,
    last_error_code text,
    created_at timestamptz NOT NULL,
    CONSTRAINT delivery_outbox_pk PRIMARY KEY (tenant_id, event_id),
    CONSTRAINT delivery_outbox_event_fk
        FOREIGN KEY (tenant_id, event_id)
        REFERENCES waybill.delivery_events (tenant_id, event_id),
    CONSTRAINT delivery_outbox_aggregate_uq
        UNIQUE (tenant_id, aggregate_type, aggregate_id, aggregate_seq),
    CONSTRAINT delivery_outbox_status_ck CHECK (
        status IN (
            'pending',
            'publishing',
            'published',
            'retryable_failed',
            'permanent_failed'
        )
    ),
    CONSTRAINT delivery_outbox_payload_ck CHECK (
        jsonb_typeof(payload) = 'object'
        AND octet_length(payload_canonical) > 0
    ),
    CONSTRAINT delivery_outbox_attempt_ck CHECK (attempt >= 0),
    CONSTRAINT delivery_outbox_fencing_ck CHECK (fencing_token >= 0),
    CONSTRAINT delivery_outbox_lease_ck CHECK (
        (lease_owner IS NULL) = (lease_deadline IS NULL)
    ),
    CONSTRAINT delivery_outbox_published_ck CHECK (
        (status = 'published' AND published_at IS NOT NULL)
        OR
        (status <> 'published' AND published_at IS NULL)
    )
);

CREATE INDEX delivery_outbox_claim_idx
    ON waybill.delivery_outbox (
        status,
        available_at,
        lease_deadline,
        created_at
    )
    WHERE status IN ('pending', 'publishing', 'retryable_failed');

CREATE TABLE waybill.delivery_inbox (
    tenant_id text NOT NULL,
    source text NOT NULL,
    event_id text NOT NULL,
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    payload_digest text NOT NULL,
    status text NOT NULL DEFAULT 'received',
    result jsonb,
    received_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz,
    CONSTRAINT delivery_inbox_pk PRIMARY KEY (tenant_id, source, event_id),
    CONSTRAINT delivery_inbox_payload_ck CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT delivery_inbox_digest_ck CHECK (payload_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT delivery_inbox_status_ck CHECK (
        status IN ('received', 'processed', 'rejected')
    ),
    CONSTRAINT delivery_inbox_result_ck CHECK (
        (status = 'received' AND result IS NULL AND processed_at IS NULL)
        OR
        (
            status IN ('processed', 'rejected')
            AND jsonb_typeof(result) = 'object'
            AND processed_at IS NOT NULL
        )
    )
);
