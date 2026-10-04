ALTER TABLE waybill.effects
    ADD COLUMN binding_schema_version smallint NOT NULL DEFAULT 0,
    ADD COLUMN adapter_id text,
    ADD COLUMN provider_contract_version text,
    ADD COLUMN provider_operation text,
    ADD COLUMN provider_scope_digest text,
    ADD COLUMN provider_request_hash text,
    ADD COLUMN key_created_at timestamptz,
    ADD COLUMN key_expires_at timestamptz,
    ADD COLUMN lookup_consistency_window_ms bigint,
    ADD COLUMN dispatch_started_at timestamptz,
    ADD COLUMN last_lookup_at timestamptz,
    ADD COLUMN external_request_id text,
    ADD COLUMN response_digest text,
    ADD COLUMN last_error_class text,
    ADD CONSTRAINT effects_binding_schema_version_ck
        CHECK (binding_schema_version IN (0, 1)),
    ADD CONSTRAINT effects_binding_shape_ck
        CHECK (
            (
                binding_schema_version = 0
                AND adapter_id IS NULL
                AND provider_contract_version IS NULL
                AND provider_operation IS NULL
                AND provider_scope_digest IS NULL
                AND provider_request_hash IS NULL
                AND key_created_at IS NULL
                AND key_expires_at IS NULL
                AND lookup_consistency_window_ms IS NULL
                AND dispatch_started_at IS NULL
            )
            OR
            (
                binding_schema_version = 1
                AND adapter_id IS NOT NULL
                AND provider_contract_version IS NOT NULL
                AND provider_operation IS NOT NULL
                AND provider_scope_digest ~ '^[0-9a-f]{64}$'
                AND provider_request_hash ~ '^[0-9a-f]{64}$'
                AND key_created_at IS NOT NULL
                AND key_expires_at > key_created_at
                AND lookup_consistency_window_ms >= 0
                AND dispatch_started_at IS NOT NULL
            )
        ),
    ADD CONSTRAINT effects_provider_metadata_ck
        CHECK (
            (response_digest IS NULL OR response_digest ~ '^[0-9a-f]{64}$')
            AND (external_request_id IS NULL OR octet_length(external_request_id) BETWEEN 1 AND 128)
            AND (last_error_class IS NULL OR octet_length(last_error_class) BETWEEN 1 AND 64)
        );

DROP INDEX waybill.effects_reconcile_idx;

CREATE INDEX effects_reconcile_idx
    ON waybill.effects (
        tenant_id,
        status,
        retry_after,
        lease_deadline,
        effect_id
    )
    WHERE status IN ('dispatching', 'unknown', 'reconciling');
