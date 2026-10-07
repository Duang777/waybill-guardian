# Wave one grounding

## Required outcomes

- Damage and loss evidence must produce a `tms.create_claim` write in the same approval batch as the existing reassign and notification writes.
- Every write remains paused before execution and covered by the existing immutable approval batch.
- A workbench URL `/waybills/:waybillID?run=:runID` must restore that exact run, including completed and rejected runs.
- Overview drill-down links must preserve a known `run_id`.
- Approval confirm/reject must converge from the returned authoritative state even when SSE is disconnected.
- Workbench SSE must close after a terminal state.

## Existing ownership and invariants

- `internal/tools` already registers, validates, canonicalizes, gates, executes, and idempotently replays `tms.create_claim`.
- `internal/approval` already treats all interrupts from one pause as one immutable batch.
- `internal/guardian` requires every approved effect to succeed before completing a run.
- `GET /api/runs/:id` already returns full snapshots for terminal runs.
- `App.tsx` currently recovers only pending approvals or operationally active runs and rewrites the URL without a run query.
- Approval POST handlers synchronously decide and resume the run before returning an approval, but the frontend ignores the response.
- `platform.TrackPoint` and catalog summaries carry `anomaly_type`; `tools.TrackingEvidence` currently drops it.

## Design constraints

- Keep model-facing write arguments frozen: `waybill_id` and `claim_type`; execution identity stays server-owned.
- Do not add a second approval lifecycle or a direct claim endpoint.
- Do not add a backend endpoint solely for exact-run URLs.
- Prefer the smallest domain addition that enforces damage/loss claim behavior without spreading a new incident representation through unrelated storage.
- URL input is untyped until parsed; preserve branded `RunID` and `WaybillID` inside React.
- Snapshot/audit events remain the replay source of truth. Do not synthesize audit events from approval responses.

## Relevant files

- `internal/agent/scenario_model.go`
- `internal/agent/online_model_acceptance_test.go`
- `internal/agent/proposal_middleware.go`
- `internal/tools/tools.go`
- `internal/tools/write.go`
- `web/src/App.tsx`
- `web/src/OverviewPage.tsx`
- `web/src/api.ts`
- `web/src/recovery.ts`
- `web/src/timeline.ts`
- `web/scripts/verify-demo.mjs`
