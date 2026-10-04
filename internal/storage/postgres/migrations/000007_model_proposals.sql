ALTER TABLE waybill.incidents
    DROP CONSTRAINT incidents_status_ck,
    ADD CONSTRAINT incidents_status_ck
        CHECK (status IN (
            'open',
            'investigating',
            'awaiting_approval',
            'executing',
            'resolved',
            'review_required',
            'manual_review',
            'failed'
        ));

ALTER TABLE waybill.runs
    DROP CONSTRAINT runs_status_ck,
    DROP CONSTRAINT runs_closed_ck,
    ADD CONSTRAINT runs_status_ck
        CHECK (status IN (
            'started',
            'investigating',
            'awaiting_approval',
            'executing',
            'completed',
            'rejected',
            'failed',
            'review_required',
            'manual_review'
        )),
    ADD CONSTRAINT runs_closed_ck
        CHECK (
            (
                status IN (
                    'completed',
                    'rejected',
                    'failed',
                    'review_required',
                    'manual_review'
                )
                AND closed_at IS NOT NULL
            )
            OR
            (
                status NOT IN (
                    'completed',
                    'rejected',
                    'failed',
                    'review_required',
                    'manual_review'
                )
                AND closed_at IS NULL
            )
        );

ALTER TABLE waybill.proposals
    ADD COLUMN accepted jsonb,
    ADD COLUMN proposal_digest text,
    ADD CONSTRAINT proposals_accepted_ck
        CHECK (
            (accepted IS NULL AND proposal_digest IS NULL)
            OR
            (
                jsonb_typeof(accepted) = 'object'
                AND proposal_digest ~ '^[0-9a-f]{64}$'
            )
        );
