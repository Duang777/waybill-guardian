CREATE TABLE waybill.delivery_source_snapshots (
    tenant_id text NOT NULL,
    snapshot_ref text NOT NULL,
    schema_version text NOT NULL,
    manifest jsonb NOT NULL,
    orders_source jsonb NOT NULL,
    depots_source jsonb NOT NULL,
    fleet_source jsonb NOT NULL,
    drivers_source jsonb NOT NULL,
    travel_source jsonb NOT NULL,
    chargers_source jsonb NOT NULL,
    policy_source jsonb NOT NULL,
    imported_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT delivery_source_snapshots_pk
        PRIMARY KEY (tenant_id, snapshot_ref),
    CONSTRAINT delivery_source_snapshots_schema_ck
        CHECK (schema_version = 'delivery.source-json.v1'),
    CONSTRAINT delivery_source_snapshots_tenant_ck
        CHECK (tenant_id <> '' AND tenant_id = btrim(tenant_id)),
    CONSTRAINT delivery_source_snapshots_ref_ck
        CHECK (snapshot_ref <> '' AND snapshot_ref = btrim(snapshot_ref)),
    CONSTRAINT delivery_source_snapshots_documents_ck
        CHECK (
            jsonb_typeof(manifest) = 'object'
            AND jsonb_typeof(orders_source) = 'object'
            AND jsonb_typeof(depots_source) = 'object'
            AND jsonb_typeof(fleet_source) = 'object'
            AND jsonb_typeof(drivers_source) = 'object'
            AND jsonb_typeof(travel_source) = 'object'
            AND jsonb_typeof(chargers_source) = 'object'
            AND jsonb_typeof(policy_source) = 'object'
        )
);

CREATE FUNCTION waybill.reject_delivery_source_snapshot_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'delivery source snapshots are immutable'
        USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER delivery_source_snapshots_immutable
    BEFORE UPDATE OR DELETE ON waybill.delivery_source_snapshots
    FOR EACH ROW
    EXECUTE FUNCTION waybill.reject_delivery_source_snapshot_mutation();
