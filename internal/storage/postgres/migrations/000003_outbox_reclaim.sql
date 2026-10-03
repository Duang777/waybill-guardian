DROP INDEX waybill.outbox_claim_idx;
DROP INDEX waybill.outbox_tenant_claim_idx;

CREATE INDEX outbox_tenant_claim_idx
    ON waybill.outbox_events (
        tenant_id,
        status,
        available_at,
        lease_deadline,
        created_at
    )
    WHERE status IN ('pending', 'publishing', 'retryable_failed');
