# RFC-001 design synthesis

## Decision

Candidate A is the base. It gives `internal/guardian.Service` ownership of complete use cases,
keeps Hastekit types inside the agent adapter, and uses one per-run JSONL as the business fact
source.

The three candidates converged on the same core design:

- Use Hastekit's native pause/resume protocol.
- Keep the business approval record outside SDK history.
- Project approval, idempotency, audit, and SSE from one append-only business journal.
- Keep a deterministic model provider for CI and explicit offline replay.
- Use an online model for the formal demo entry points.
- Require downstream idempotency for real writes.

## Adapted ideas

Candidate B supplied the final middleware order:

```text
audit -> approval guard -> idempotency -> tool
```

The audit wrapper stays outermost so denied and duplicate calls remain observable.

Candidate C supplied the fixture-provider shape. The provider embeds Hastekit's base provider,
implements the Responses path used by the Agent, and returns explicit unsupported errors for
other model capabilities.

## Rejected ideas

- Separate approval, idempotency, and audit JSONL files. They create cross-file commit windows.
- SQLite in the first milestone. It is the production migration target, but the issues require
  inspectable JSONL.
- Blocking a goroutine while waiting for approval. Hastekit already persists a paused run.
- Sending dotted contract names directly to every model provider. A registry maps them to
  provider-safe wire names.
- Treating SDK stream chunks as audit records. They are not a durable source.

## Verification

The design was checked against issues #2 through #14, the v0.0.24 source study, the
architecture red flags, and six criteria: requirement coverage, state ownership, crash
behavior, implementation scope, transport clarity, and testability.
