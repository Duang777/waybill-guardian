INSERT INTO waybill.run_quarantines (
    tenant_id,
    run_id,
    reason,
    detail_code,
    observed_seq,
    observed_hash
)
SELECT DISTINCT
    run.tenant_id,
    run.run_id,
    'legacy agent history was removed by the privacy schema upgrade',
    'history_privacy_schema_upgrade',
    run.last_audit_seq,
    run.last_audit_hash
FROM waybill.runs run
JOIN waybill.agent_checkpoints checkpoint
  ON checkpoint.tenant_id = run.tenant_id
 AND checkpoint.thread_id = run.run_id
WHERE run.status NOT IN ('completed', 'rejected', 'failed', 'manual_review')
ON CONFLICT (tenant_id, run_id) DO NOTHING;

UPDATE waybill.runs run
SET status = 'manual_review',
    sdk_run_id = NULL,
    checkpoint_version = 0,
    lease_owner = NULL,
    lease_deadline = NULL,
    updated_at = clock_timestamp(),
    closed_at = COALESCE(run.closed_at, clock_timestamp())
FROM waybill.run_quarantines quarantine
WHERE quarantine.tenant_id = run.tenant_id
  AND quarantine.run_id = run.run_id
  AND quarantine.detail_code = 'history_privacy_schema_upgrade';

UPDATE waybill.incidents incident
SET status = 'manual_review',
    updated_at = clock_timestamp()
FROM waybill.runs run
JOIN waybill.run_quarantines quarantine
  ON quarantine.tenant_id = run.tenant_id
 AND quarantine.run_id = run.run_id
WHERE incident.tenant_id = run.tenant_id
  AND incident.incident_id = run.incident_id
  AND quarantine.detail_code = 'history_privacy_schema_upgrade';

UPDATE waybill.runs run
SET sdk_run_id = NULL,
    checkpoint_version = 0
WHERE EXISTS (
    SELECT 1
    FROM waybill.agent_checkpoints checkpoint
    WHERE checkpoint.tenant_id = run.tenant_id
      AND checkpoint.thread_id = run.run_id
);

DELETE FROM waybill.agent_summaries;
DELETE FROM waybill.agent_checkpoints;

ALTER TABLE waybill.agent_checkpoints
    DROP COLUMN metadata,
    ADD COLUMN privacy_schema_version smallint NOT NULL,
    ADD CONSTRAINT agent_checkpoints_privacy_schema_ck
        CHECK (privacy_schema_version = 1);

ALTER TABLE waybill.agent_summaries
    ADD COLUMN privacy_schema_version smallint NOT NULL,
    ADD CONSTRAINT agent_summaries_privacy_schema_ck
        CHECK (privacy_schema_version = 1);
