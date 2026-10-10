CREATE TABLE waybill.delivery_problems (
    tenant_id text NOT NULL,
    problem_id text NOT NULL,
    latest_version bigint NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT delivery_problems_pk PRIMARY KEY (tenant_id, problem_id),
    CONSTRAINT delivery_problems_version_ck CHECK (latest_version > 0)
);

CREATE TABLE waybill.delivery_problem_versions (
    tenant_id text NOT NULL,
    problem_id text NOT NULL,
    version bigint NOT NULL,
    problem_digest text NOT NULL,
    policy_digest text NOT NULL,
    commitment_digest text NOT NULL,
    manifest_digest text NOT NULL,
    problem_artifact_digest text NOT NULL,
    source_profile text NOT NULL,
    source_ref text NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT delivery_problem_versions_pk
        PRIMARY KEY (tenant_id, problem_id, version),
    CONSTRAINT delivery_problem_versions_problem_fk
        FOREIGN KEY (tenant_id, problem_id)
        REFERENCES waybill.delivery_problems (tenant_id, problem_id),
    CONSTRAINT delivery_problem_versions_version_ck CHECK (version > 0),
    CONSTRAINT delivery_problem_versions_digest_ck CHECK (
        problem_digest ~ '^[0-9a-f]{64}$'
        AND policy_digest ~ '^[0-9a-f]{64}$'
        AND commitment_digest ~ '^[0-9a-f]{64}$'
        AND manifest_digest ~ '^[0-9a-f]{64}$'
        AND problem_artifact_digest ~ '^[0-9a-f]{64}$'
    )
);

CREATE TABLE waybill.delivery_runs (
    tenant_id text NOT NULL,
    run_id text NOT NULL,
    problem_id text NOT NULL,
    problem_version bigint NOT NULL,
    problem_digest text NOT NULL,
    solver_profile text NOT NULL,
    config_digest text NOT NULL,
    status text NOT NULL,
    version bigint NOT NULL,
    cancel_requested_at timestamptz,
    checkpoint_digest text,
    result_revision_id text,
    failure_code text,
    lease_owner text,
    lease_deadline timestamptz,
    fencing_token bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    closed_at timestamptz,
    CONSTRAINT delivery_runs_pk PRIMARY KEY (tenant_id, run_id),
    CONSTRAINT delivery_runs_problem_fk
        FOREIGN KEY (tenant_id, problem_id, problem_version)
        REFERENCES waybill.delivery_problem_versions (tenant_id, problem_id, version),
    CONSTRAINT delivery_runs_digest_ck CHECK (
        problem_digest ~ '^[0-9a-f]{64}$'
        AND config_digest ~ '^[0-9a-f]{64}$'
        AND (checkpoint_digest IS NULL OR checkpoint_digest ~ '^[0-9a-f]{64}$')
    ),
    CONSTRAINT delivery_runs_status_ck CHECK (status IN (
        'requested',
        'queued',
        'solving',
        'validating',
        'succeeded',
        'candidate_rejected',
        'exhausted',
        'aborted',
        'failed',
        'cancelled',
        'manual_review'
    )),
    CONSTRAINT delivery_runs_version_ck CHECK (version > 0),
    CONSTRAINT delivery_runs_fencing_ck CHECK (fencing_token >= 0),
    CONSTRAINT delivery_runs_lease_ck CHECK (
        (lease_owner IS NULL) = (lease_deadline IS NULL)
    ),
    CONSTRAINT delivery_runs_closed_ck CHECK (
        (
            status IN (
                'succeeded',
                'candidate_rejected',
                'exhausted',
                'aborted',
                'failed',
                'cancelled',
                'manual_review'
            )
            AND closed_at IS NOT NULL
            AND lease_owner IS NULL
            AND lease_deadline IS NULL
        )
        OR
        (
            status NOT IN (
                'succeeded',
                'candidate_rejected',
                'exhausted',
                'aborted',
                'failed',
                'cancelled',
                'manual_review'
            )
            AND closed_at IS NULL
        )
    )
);

CREATE INDEX delivery_runs_claim_idx
    ON waybill.delivery_runs (
        status,
        lease_deadline,
        created_at,
        tenant_id,
        run_id
    )
    WHERE status IN ('queued', 'solving', 'validating');

CREATE TABLE waybill.delivery_plans (
    tenant_id text NOT NULL,
    plan_id text NOT NULL,
    problem_id text NOT NULL,
    active_revision_id text,
    active_version bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT delivery_plans_pk PRIMARY KEY (tenant_id, plan_id),
    CONSTRAINT delivery_plans_problem_fk
        FOREIGN KEY (tenant_id, problem_id)
        REFERENCES waybill.delivery_problems (tenant_id, problem_id),
    CONSTRAINT delivery_plans_active_version_ck CHECK (active_version >= 0),
    CONSTRAINT delivery_plans_active_pair_ck CHECK (
        (active_revision_id IS NULL AND active_version = 0)
        OR
        (active_revision_id IS NOT NULL AND active_version > 0)
    )
);

CREATE TABLE waybill.delivery_plan_revisions (
    tenant_id text NOT NULL,
    revision_id text NOT NULL,
    plan_id text NOT NULL,
    base_revision_id text,
    run_id text NOT NULL,
    problem_digest text NOT NULL,
    policy_digest text NOT NULL,
    commitment_digest text NOT NULL,
    plan_artifact_digest text NOT NULL,
    plan_digest text NOT NULL,
    validation_artifact_digest text NOT NULL,
    validation_report_digest text NOT NULL,
    effect_set_artifact_digest text,
    effect_set_digest text,
    status text NOT NULL,
    version bigint NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT delivery_plan_revisions_pk
        PRIMARY KEY (tenant_id, revision_id),
    CONSTRAINT delivery_plan_revisions_plan_fk
        FOREIGN KEY (tenant_id, plan_id)
        REFERENCES waybill.delivery_plans (tenant_id, plan_id),
    CONSTRAINT delivery_plan_revisions_base_fk
        FOREIGN KEY (tenant_id, base_revision_id)
        REFERENCES waybill.delivery_plan_revisions (tenant_id, revision_id),
    CONSTRAINT delivery_plan_revisions_run_fk
        FOREIGN KEY (tenant_id, run_id)
        REFERENCES waybill.delivery_runs (tenant_id, run_id),
    CONSTRAINT delivery_plan_revisions_run_uq UNIQUE (tenant_id, run_id),
    CONSTRAINT delivery_plan_revisions_digest_ck CHECK (
        problem_digest ~ '^[0-9a-f]{64}$'
        AND policy_digest ~ '^[0-9a-f]{64}$'
        AND commitment_digest ~ '^[0-9a-f]{64}$'
        AND plan_artifact_digest ~ '^[0-9a-f]{64}$'
        AND plan_digest ~ '^[0-9a-f]{64}$'
        AND validation_artifact_digest ~ '^[0-9a-f]{64}$'
        AND validation_report_digest ~ '^[0-9a-f]{64}$'
        AND (
            (effect_set_artifact_digest IS NULL AND effect_set_digest IS NULL)
            OR
            (
                effect_set_artifact_digest ~ '^[0-9a-f]{64}$'
                AND effect_set_digest ~ '^[0-9a-f]{64}$'
            )
        )
    ),
    CONSTRAINT delivery_plan_revisions_status_ck CHECK (status IN (
        'candidate',
        'validated',
        'awaiting_approval',
        'approved',
        'applying',
        'active',
        'rejected',
        'expired',
        'stale',
        'reconciliation_required',
        'partially_applied',
        'superseded',
        'completed'
    )),
    CONSTRAINT delivery_plan_revisions_version_ck CHECK (version > 0)
);

ALTER TABLE waybill.delivery_plans
    ADD CONSTRAINT delivery_plans_active_revision_fk
        FOREIGN KEY (tenant_id, active_revision_id)
        REFERENCES waybill.delivery_plan_revisions (tenant_id, revision_id);

ALTER TABLE waybill.delivery_runs
    ADD CONSTRAINT delivery_runs_result_revision_fk
        FOREIGN KEY (tenant_id, result_revision_id)
        REFERENCES waybill.delivery_plan_revisions (tenant_id, revision_id);

CREATE TABLE waybill.delivery_command_results (
    tenant_id text NOT NULL,
    operation text NOT NULL,
    idempotency_key text NOT NULL,
    request_digest text NOT NULL,
    resource_type text,
    resource_id text,
    response jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    CONSTRAINT delivery_command_results_pk
        PRIMARY KEY (tenant_id, operation, idempotency_key),
    CONSTRAINT delivery_command_results_digest_ck
        CHECK (request_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT delivery_command_results_shape_ck CHECK (
        (
            response IS NULL
            AND resource_type IS NULL
            AND resource_id IS NULL
            AND completed_at IS NULL
        )
        OR
        (
            jsonb_typeof(response) = 'object'
            AND resource_type IS NOT NULL
            AND resource_id IS NOT NULL
            AND completed_at IS NOT NULL
        )
    )
);

CREATE TABLE waybill.delivery_artifact_refs (
    tenant_id text NOT NULL,
    digest text NOT NULL,
    owner_type text NOT NULL,
    owner_id text NOT NULL,
    role text NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT delivery_artifact_refs_pk
        PRIMARY KEY (tenant_id, digest, owner_type, owner_id, role),
    CONSTRAINT delivery_artifact_refs_digest_ck
        CHECK (digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT delivery_artifact_refs_owner_ck
        CHECK (owner_type IN ('problem_version', 'run', 'plan_revision', 'execution'))
);

CREATE INDEX delivery_artifact_refs_owner_idx
    ON waybill.delivery_artifact_refs (tenant_id, owner_type, owner_id);
