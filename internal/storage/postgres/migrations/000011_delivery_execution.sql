ALTER TABLE waybill.delivery_runs
    ADD COLUMN requested_by text NOT NULL DEFAULT 'system:unknown';

CREATE TABLE waybill.delivery_approvals (
    tenant_id text NOT NULL,
    approval_id text NOT NULL,
    plan_id text NOT NULL,
    revision_id text NOT NULL,
    base_revision_id text,
    active_version bigint NOT NULL,
    problem_digest text NOT NULL,
    policy_digest text NOT NULL,
    commitment_digest text NOT NULL,
    plan_digest text NOT NULL,
    validation_report_digest text NOT NULL,
    effect_set_artifact_digest text NOT NULL,
    effect_set_digest text NOT NULL,
    status text NOT NULL,
    version bigint NOT NULL,
    requested_by text NOT NULL,
    plan_created_by text NOT NULL,
    reason text NOT NULL,
    requested_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    decided_by text,
    decided_at timestamptz,
    reject_reason text,
    override_reason text,
    execution_id text,
    CONSTRAINT delivery_approvals_pk PRIMARY KEY (tenant_id, approval_id),
    CONSTRAINT delivery_approvals_plan_fk
        FOREIGN KEY (tenant_id, plan_id)
        REFERENCES waybill.delivery_plans (tenant_id, plan_id),
    CONSTRAINT delivery_approvals_revision_fk
        FOREIGN KEY (tenant_id, revision_id)
        REFERENCES waybill.delivery_plan_revisions (tenant_id, revision_id),
    CONSTRAINT delivery_approvals_digest_ck CHECK (
        problem_digest ~ '^[0-9a-f]{64}$'
        AND policy_digest ~ '^[0-9a-f]{64}$'
        AND commitment_digest ~ '^[0-9a-f]{64}$'
        AND plan_digest ~ '^[0-9a-f]{64}$'
        AND validation_report_digest ~ '^[0-9a-f]{64}$'
        AND effect_set_artifact_digest ~ '^[0-9a-f]{64}$'
        AND effect_set_digest ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT delivery_approvals_status_ck CHECK (
        status IN ('pending', 'confirmed', 'rejected', 'expired', 'stale')
    ),
    CONSTRAINT delivery_approvals_version_ck CHECK (version > 0),
    CONSTRAINT delivery_approvals_expiry_ck CHECK (expires_at > requested_at),
    CONSTRAINT delivery_approvals_decision_ck CHECK (
        (
            status = 'pending'
            AND decided_by IS NULL
            AND decided_at IS NULL
            AND reject_reason IS NULL
            AND override_reason IS NULL
            AND execution_id IS NULL
        )
        OR
        (
            status = 'confirmed'
            AND decided_by IS NOT NULL
            AND decided_at IS NOT NULL
            AND reject_reason IS NULL
            AND execution_id IS NOT NULL
        )
        OR
        (
            status = 'rejected'
            AND decided_by IS NOT NULL
            AND decided_at IS NOT NULL
            AND reject_reason IS NOT NULL
            AND override_reason IS NULL
            AND execution_id IS NULL
        )
        OR status IN ('expired', 'stale')
    )
);

CREATE UNIQUE INDEX delivery_approvals_pending_revision_uq
    ON waybill.delivery_approvals (tenant_id, revision_id)
    WHERE status = 'pending';

CREATE TABLE waybill.delivery_execution_reservations (
    tenant_id text NOT NULL,
    execution_id text NOT NULL,
    approval_id text NOT NULL,
    plan_id text NOT NULL,
    revision_id text NOT NULL,
    base_revision_id text,
    active_version bigint NOT NULL,
    effect_set_digest text NOT NULL,
    status text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    CONSTRAINT delivery_execution_reservations_pk
        PRIMARY KEY (tenant_id, execution_id),
    CONSTRAINT delivery_execution_reservations_approval_uq
        UNIQUE (tenant_id, approval_id),
    CONSTRAINT delivery_execution_reservations_approval_fk
        FOREIGN KEY (tenant_id, approval_id)
        REFERENCES waybill.delivery_approvals (tenant_id, approval_id),
    CONSTRAINT delivery_execution_reservations_plan_fk
        FOREIGN KEY (tenant_id, plan_id)
        REFERENCES waybill.delivery_plans (tenant_id, plan_id),
    CONSTRAINT delivery_execution_reservations_revision_fk
        FOREIGN KEY (tenant_id, revision_id)
        REFERENCES waybill.delivery_plan_revisions (tenant_id, revision_id),
    CONSTRAINT delivery_execution_reservations_digest_ck
        CHECK (effect_set_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT delivery_execution_reservations_status_ck CHECK (
        status IN (
            'reserved',
            'reconciliation_required',
            'committed',
            'released'
        )
    ),
    CONSTRAINT delivery_execution_reservations_completion_ck CHECK (
        (
            status IN ('reserved', 'reconciliation_required')
            AND completed_at IS NULL
        )
        OR
        (
            status IN ('committed', 'released')
            AND completed_at IS NOT NULL
        )
    )
);

CREATE UNIQUE INDEX delivery_execution_reservations_live_plan_uq
    ON waybill.delivery_execution_reservations (tenant_id, plan_id)
    WHERE status IN ('reserved', 'reconciliation_required');

CREATE TABLE waybill.delivery_executions (
    tenant_id text NOT NULL,
    execution_id text NOT NULL,
    approval_id text NOT NULL,
    plan_id text NOT NULL,
    revision_id text NOT NULL,
    effect_set_digest text NOT NULL,
    status text NOT NULL,
    version bigint NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    CONSTRAINT delivery_executions_pk PRIMARY KEY (tenant_id, execution_id),
    CONSTRAINT delivery_executions_approval_fk
        FOREIGN KEY (tenant_id, approval_id)
        REFERENCES waybill.delivery_approvals (tenant_id, approval_id),
    CONSTRAINT delivery_executions_approval_uq UNIQUE (tenant_id, approval_id),
    CONSTRAINT delivery_executions_plan_fk
        FOREIGN KEY (tenant_id, plan_id)
        REFERENCES waybill.delivery_plans (tenant_id, plan_id),
    CONSTRAINT delivery_executions_revision_fk
        FOREIGN KEY (tenant_id, revision_id)
        REFERENCES waybill.delivery_plan_revisions (tenant_id, revision_id),
    CONSTRAINT delivery_executions_reservation_fk
        FOREIGN KEY (tenant_id, execution_id)
        REFERENCES waybill.delivery_execution_reservations (
            tenant_id,
            execution_id
        ),
    CONSTRAINT delivery_executions_digest_ck
        CHECK (effect_set_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT delivery_executions_status_ck CHECK (
        status IN (
            'prepared',
            'executing',
            'committed',
            'reconciliation_required',
            'partially_applied',
            'manual_review'
        )
    ),
    CONSTRAINT delivery_executions_version_ck CHECK (version > 0),
    CONSTRAINT delivery_executions_completed_ck CHECK (
        (status = 'committed' AND completed_at IS NOT NULL)
        OR
        (status <> 'committed' AND completed_at IS NULL)
    )
);

ALTER TABLE waybill.delivery_execution_reservations
    ADD CONSTRAINT delivery_execution_reservations_execution_fk
        FOREIGN KEY (tenant_id, execution_id)
        REFERENCES waybill.delivery_executions (tenant_id, execution_id)
        DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE waybill.delivery_approvals
    ADD CONSTRAINT delivery_approvals_execution_fk
        FOREIGN KEY (tenant_id, execution_id)
        REFERENCES waybill.delivery_executions (tenant_id, execution_id)
        DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE waybill.delivery_effects (
    tenant_id text NOT NULL,
    effect_id text NOT NULL,
    execution_id text NOT NULL,
    revision_id text NOT NULL,
    ordinal integer NOT NULL,
    action text NOT NULL,
    target text NOT NULL,
    parameters jsonb NOT NULL,
    parameters_digest text NOT NULL,
    required boolean NOT NULL,
    adapter_id text NOT NULL,
    contract_version text NOT NULL,
    adapter_binding jsonb NOT NULL,
    adapter_binding_digest text NOT NULL,
    idempotency_key text NOT NULL,
    request_digest text NOT NULL,
    key_created_at timestamptz NOT NULL,
    key_expires_at timestamptz NOT NULL,
    lookup_consistency_window_seconds bigint NOT NULL,
    status text NOT NULL,
    next_operation text NOT NULL,
    attempt integer NOT NULL DEFAULT 0,
    external_ref text,
    response_digest text,
    error_code text,
    retry_at timestamptz,
    dispatch_started_at timestamptz,
    last_lookup_at timestamptz,
    lease_owner text,
    lease_deadline timestamptz,
    fencing_token bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL,
    CONSTRAINT delivery_effects_pk PRIMARY KEY (tenant_id, effect_id),
    CONSTRAINT delivery_effects_execution_fk
        FOREIGN KEY (tenant_id, execution_id)
        REFERENCES waybill.delivery_executions (tenant_id, execution_id),
    CONSTRAINT delivery_effects_revision_fk
        FOREIGN KEY (tenant_id, revision_id)
        REFERENCES waybill.delivery_plan_revisions (tenant_id, revision_id),
    CONSTRAINT delivery_effects_ordinal_uq
        UNIQUE (tenant_id, execution_id, ordinal),
    CONSTRAINT delivery_effects_key_uq
        UNIQUE (tenant_id, adapter_id, idempotency_key),
    CONSTRAINT delivery_effects_digest_ck CHECK (
        parameters_digest ~ '^[0-9a-f]{64}$'
        AND adapter_binding_digest ~ '^[0-9a-f]{64}$'
        AND request_digest ~ '^[0-9a-f]{64}$'
        AND (response_digest IS NULL OR response_digest ~ '^[0-9a-f]{64}$')
    ),
    CONSTRAINT delivery_effects_parameters_ck CHECK (
        jsonb_typeof(parameters) = 'object'
        AND jsonb_typeof(adapter_binding) = 'object'
    ),
    CONSTRAINT delivery_effects_status_ck CHECK (
        status IN (
            'prepared',
            'dispatching',
            'succeeded',
            'unknown',
            'reconciling',
            'retry_wait',
            'permanent_failed',
            'manual_review'
        )
    ),
    CONSTRAINT delivery_effects_attempt_ck CHECK (attempt >= 0),
    CONSTRAINT delivery_effects_window_ck CHECK (
        lookup_consistency_window_seconds >= 0
    ),
    CONSTRAINT delivery_effects_operation_ck CHECK (
        next_operation IN ('dispatch', 'lookup')
    ),
    CONSTRAINT delivery_effects_key_expiry_ck CHECK (
        key_expires_at > key_created_at
    ),
    CONSTRAINT delivery_effects_fencing_ck CHECK (fencing_token >= 0),
    CONSTRAINT delivery_effects_lease_ck CHECK (
        (lease_owner IS NULL) = (lease_deadline IS NULL)
    ),
    CONSTRAINT delivery_effects_dispatch_intent_ck CHECK (
        (
            status IN ('prepared', 'manual_review')
            AND dispatch_started_at IS NULL
            AND attempt = 0
        )
        OR
        (
            status <> 'prepared'
            AND dispatch_started_at IS NOT NULL
            AND attempt > 0
        )
    )
);

CREATE INDEX delivery_effects_claim_idx
    ON waybill.delivery_effects (
        status,
        retry_at,
        lease_deadline,
        tenant_id,
        execution_id,
        ordinal
    )
    WHERE status IN (
        'prepared',
        'dispatching',
        'unknown',
        'reconciling',
        'retry_wait'
    );
