# Wave one design

## Problem

Damage and loss facts already exist on platform tracking points, but the model-facing tracking DTO drops `anomaly_type`. The write stack already supports `tms.create_claim`; only proposal generation and validation are incomplete. The browser can fetch any terminal run snapshot, but its route carries only a waybill, decision POST responses are discarded, and terminal SSE connections remain open.

## Usage

```text
/waybills/YD2026100007?run=<run-id>
```

- A URL with `run` loads that exact snapshot before heuristic recovery.
- A URL without `run` keeps pending-approval then active-run recovery.
- Damage and loss runs pause once with reassign, claim, and notification writes in one approval batch.
- Confirm and reject reload the same run snapshot after the POST succeeds.

## Shape

### Claim policy

- Add bounded `anomaly_type` to `tools.TrackingEvidence`.
- Keep a private closed damage/loss claim type and classifier in `internal/agent`.
- `ScenarioModel` appends one claim call per distinct claim-bearing anomaly type before pause and requires the corresponding successful result before reporting success.
- The compatible online fake follows the same evidence rule.
- `ProposalBoundary` derives required claims from the latest successful tracking result and rejects missing, extra, duplicate, wrong-waybill, or wrong-type claim calls. The existing one-repair flow remains the only correction path.
- The online prompt states the same rule and requires `/points/{index}/anomaly_type` evidence.
- Do not change approval, effect identity, idempotency, persistence, HTTP routes, or real-platform capability registration.

### Exact run target

```ts
type WorkbenchTarget =
  | { kind: "waybill"; waybillID: WaybillID }
  | { kind: "run"; waybillID: WaybillID; runID: RunID };
```

- A new `workbench-route.ts` owns URL parsing and formatting.
- Raw path/query values are parsed with exported Zod schemas; malformed or duplicate run values are invalid.
- Explicit run targets bypass active/pending discovery and verify both run and waybill identity against the snapshot.
- Recovery decisions retain both `runID` and `waybillID`.
- Overview links prefer live run projections, then snapshot `run_id`, then waybill-only navigation.

### Decision convergence and SSE

- Confirm/reject use the approval response only for run identity, then hydrate the authoritative snapshot.
- No UI code synthesizes audit events.
- `openTimeline` delivers a terminal event, closes its `EventSource`, and reports disconnected.
- Terminal snapshots never open SSE.
- A failed snapshot refresh remains retryable by exact run identity and never resubmits the decision.

## Synthesis decision

Candidate 3 is the base. It was the only candidate that preserved the immutable pause batch, enforced online claim alignment before pause, and kept snapshot events as replay truth without broad storage changes.

Grafted from candidate 1:

- Keep claim classification private to `internal/agent` rather than exporting a public `internal/tools.RequiredClaims` policy.
- Test a stale overview snapshot where a newer local run projection must win, and test exact replay of an older terminal run while a newer run is active.

Rejected:

- Prompt-only enforcement because a valid online response could omit a required claim.
- Server-appending missing model calls because it changes proposal authorship inside validation middleware; the existing repair path is explicit and auditable.
- Guardian-added approval items because they would not correspond to paused SDK calls.
- New backend replay endpoints because `GET /api/runs/:id` already owns the capability.
- Domain/storage incident-type expansion because the required evidence already exists at the tracking boundary.

## Tradeoffs

- We accept one repair attempt for a noncompliant online response in exchange for keeping write authorship explicit.
- We accept a snapshot GET after each decision in exchange for deterministic convergence without SSE.
- We accept exact, case-sensitive `damage` and `loss` matching in exchange for a closed, documented claim policy.
- We accept client-side terminal SSE closure; server-side terminal subscription policy remains separate hardening.

## Verification

- Go tests: tracking evidence, claim classifier, offline pause batch, online protocol fake, proposal-boundary repair/alignment, no pre-approval writes, exactly-once approved claim.
- Vitest: route parse/format, recovery identity, terminal EventSource closure.
- Browser: disconnected decision convergence, completed/rejected exact reload, older terminal run versus newer active run, overview links preserving projected run IDs.

## Next implementation step

Add failing claim evidence and proposal-alignment tests, then implement the backend claim path before changing browser navigation.
