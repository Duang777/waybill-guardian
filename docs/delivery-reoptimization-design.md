# Delivery dynamic reoptimization design

## Problem

Dynamic reoptimization turns operational changes into an immutable successor
problem and plan revision. The current reducer is deterministic only inside one
call: its watermark is opaque, duplicate detection is batch-local, event time is
used as ordering, and cancellation can erase executed tasks or in-transit cargo.

The implementation must extend, rather than duplicate, the Delivery lifecycle:

- Agent-2 owns `ProblemVersion`, `OptimizationRun`, `DispatchPlan`,
  `PlanRevision`, PostgreSQL transactions, approval, effects, and activation.
- The artifact store owns immutable problem, plan, validation, comparison, and
  fact-application evidence.
- Agent-3 consumes a strict six-kind comparison projection.
- Guardian and Delivery exchange versioned CloudEvents through an
  anti-corruption mapper; neither imports the other's aggregate types.

## Usage

### Record facts

Boundary adapters parse authenticated input into typed Delivery facts. The
tenant comes from the authenticated inbox or principal, never from event data.

```go
receipt, replay, err := delivery.Commands().RecordOperationalFacts(
	ctx,
	service.RecordOperationalFacts{
		TenantID:       principal.TenantID,
		ProblemID:      problemID,
		Actor:          service.Actor{Subject: "system:guardian-inbox"},
		IdempotencyKey: service.IdempotencyKey(event.Source() + ":" + event.ID()),
		Facts:          facts,
	},
)
```

An exact replay returns the prior receipt. A fact above a gap is retained but
does not advance the contiguous frontier. Reusing a fact identity or stream
position with different canonical bytes quarantines that stream.

### Request a successor

The application chooses the latest contiguous, unapplied frontier. Callers do
not coordinate stream positions, load snapshots, or construct commitments.

```go
outcome, replay, err := delivery.Commands().RequestReoptimization(
	ctx,
	service.RequestReoptimization{
		TenantID:       principal.TenantID,
		PlanID:         planID,
		Actor:          service.Actor{Subject: principal.Subject},
		IdempotencyKey: requestID,
		SolverProfile:  "builtin-production",
		OverrideApprovalID: optionalOverrideApprovalID,
	},
)
```

The command returns one of:

- a queued successor problem and optimization run;
- an idempotent no-op when no contiguous unapplied facts exist;
- a typed fact gap or conflict;
- `manual_review` with the protected objects that make a fact impossible;
- a freeze-override proposal containing exact before and after commitments.

### Publish and execute

The existing run worker solves and validates the successor, computes the
complete revision comparison and effect set, writes and verifies their
artifacts, then publishes a successor revision under the existing
`DispatchPlan`.

Confirming plan approval reserves the current active tuple before any effect is
claimable:

```go
reservation, err := store.DecideAndCreateExecution(ctx, service.DecideExecutionTx{
	ExpectedActiveRevisionID: approval.Binding.BaseRevisionID,
	ExpectedActiveVersion:    approval.Binding.ActiveVersion,
	// Existing approval, execution, and effect fields omitted.
})
```

Only the reservation owner may dispatch effects. After all required effects
succeed, the existing activation transaction performs a second CAS and marks
the reservation committed.

## Domain shape

### Ordered facts and frontier

```go
const (
	OperationalFactSchemaVersion = "delivery.operational-fact.v2"
	FactFrontierSchemaVersion     = "delivery.fact-frontier.v1"
)

type FactID string

type FactStream struct {
	SourceSystem string `json:"source_system"`
	Name         string `json:"name"`
	Partition    string `json:"partition"`
}

type FactPosition struct {
	Stream   FactStream `json:"stream"`
	Epoch    uint64     `json:"epoch"`
	Sequence uint64     `json:"sequence"`
}

type OperationalFactHeader struct {
	SchemaVersion string       `json:"schema_version"`
	FactID        FactID       `json:"fact_id"`
	Position      FactPosition `json:"position"`
	OccurredAt    time.Time    `json:"occurred_at"`
	ObservedAt    time.Time    `json:"observed_at"`
}

type FactFrontier struct {
	SchemaVersion string         `json:"schema_version"`
	Positions     []FactPosition `json:"positions"`
	Digest        ArtifactDigest `json:"digest"`
}
```

`FactFrontier.Positions` contains one contiguous head per stream and is sorted
by source, name, partition, and epoch. An epoch transition requires a
canonical, authenticated stream-open record that binds the prior terminal
position and frontier digest.

`CommitmentSet.FactWatermark` remains a string for cross-branch compatibility.
Its value is the lowercase SHA-256 digest of the canonical `FactFrontier`.
The full frontier is retained as an artifact and in per-version PostgreSQL
rows.

Fact identity has two tenant-scoped uniqueness constraints:

```text
(tenant, problem, source, stream, partition, epoch, sequence)
(tenant, problem, fact_id)
```

The ledger compares a canonical fact digest derived by Delivery. Producers
cannot provide or override that digest.

### Ledger outcomes

```go
type FactDisposition string

const (
	FactAppended   FactDisposition = "appended"
	FactReplayed   FactDisposition = "replayed"
	FactPendingGap FactDisposition = "pending_gap"
	FactConflict  FactDisposition = "conflict"
)

type FactGap struct {
	Stream   FactStream
	Epoch    uint64
	Expected uint64
	Observed uint64
}

type FactConflictRecord struct {
	Position       FactPosition
	ExistingFactID FactID
	IncomingFactID FactID
	ExistingDigest ArtifactDigest
	IncomingDigest ArtifactDigest
	Code           string
}
```

Rules:

1. The same position, fact ID, and digest is an exact replay.
2. Reusing a position or fact ID with different content is a durable conflict.
3. A sequence above `contiguous + 1` is retained as pending.
4. Filling a gap promotes the longest contiguous prefix in one transaction.
5. A missing retained row below the head is ledger corruption.
6. Conflict resolution is a separate audited command; rows are never rewritten.
7. The applied frontier advances only with successor commit.

### Successor builder

```go
type SuccessorBuildInput struct {
	BaseProblem        domain.ProblemSnapshot
	ActiveRevisionID   domain.PlanRevisionID
	ActivePlan         domain.Plan
	BaseFrontier       domain.FactFrontier
	TargetFrontier     domain.FactFrontier
	Facts              []domain.LedgerFact
	Override           *VerifiedFreezeOverride
	CreatedAt          time.Time
}

type SuccessorBuildOutput struct {
	Problem           domain.ProblemSnapshot
	Frontier          domain.FactFrontier
	AppliedFacts      []domain.FactRef
	Applications      []domain.FactApplication
	OverrideUse       *domain.FreezeOverrideUse
	ApplicationDigest domain.ArtifactDigest
}

func BuildSuccessor(SuccessorBuildInput) (SuccessorBuildOutput, error)
```

The builder verifies all base plan and problem digests, exact coverage of
`(base, target]`, canonical stream order, timestamps, and override binding. It
then folds facts and rebuilds the snapshot through `BuildProblemSnapshot`.
Event time is evidence, not an ordering key.

Facts that make incompatible claims about the same terminal state return a
semantic conflict. The reducer never applies last-writer-wins across streams.

### Protected execution closure

Before cancellation or resource changes are applied, the reducer builds a
protected closure containing:

- executed tasks;
- the currently served stop and completed route prefix;
- occurred pickups and their delivery or approved return tasks;
- in-transit cargo, fulfillment units, current placement, and unload tasks;
- pinned vehicle position, driver duty prefix, and energy state;
- predecessor records needed to keep the retained graph valid.

Cancellation removes only untouched work outside this closure. Executed and
in-transit commitments are append-only until a typed completion, unload, or
transfer fact proves the physical state changed. A cancellation that attempts
to erase protected work enters `manual_review`.

### Freeze override

Freeze override is an independent, pre-solve approval. It is not a Boolean
flag and is not implied by final plan approval.

```go
type FreezeOverrideScope struct {
	TaskID                 TaskID
	Before                 FrozenTaskCommitment
	AllowVehicleChange     bool
	AllowedVehicleIDs      []VehicleID
	AllowDriverChange      bool
	AllowedDriverIDs       []DriverID
	MaxSequenceShift       uint32
	MaxETADriftSeconds     int64
	AllowCargoRepack       bool
	CargoIDs               []CargoID
	AllowedCompartmentIDs  []CompartmentID
}

type FreezeOverrideGrant struct {
	SchemaVersion       string
	ApprovalID          ApprovalID
	TenantID            TenantID
	PlanID              PlanID
	BaseRevisionID      PlanRevisionID
	BaseActiveVersion   uint64
	ProblemDigest       ArtifactDigest
	PolicyDigest        ArtifactDigest
	TargetFrontierDigest ArtifactDigest
	Scopes              []FreezeOverrideScope
	RequestedBy         string
	ApprovedBy          string
	Reason              string
	ApprovedAt          time.Time
	ExpiresAt           time.Time
	Digest              ArtifactDigest
}

type FreezeOverrideUse struct {
	GrantDigest ArtifactDigest
	TaskChanges []FrozenCommitmentChange
	LoadChanges []CargoPlacementChange
	Digest      ArtifactDigest
}
```

Only a store-backed verifier can construct `VerifiedFreezeOverride`; its
fields are private. Verification requires:

- a confirmed, unexpired persisted approval;
- exact base revision, active version, problem, policy, and frontier binding;
- exact base commitments and bounded dimensions, IDs, sequence shift, ETA
  drift, and cargo scope, with no wildcard scope;
- `ApprovedBy != RequestedBy` and `ApprovedBy != PlanCreatedBy`;
- `delivery.freeze_override.approve` permission and a non-empty reason.

The successor commit repeats these checks under lock. Executed tasks cannot be
overridden. The solver may search only inside the approved bounds. The
comparison compiler then emits a `FreezeOverrideUse` with exact before and
after values and rejects any use outside the grant. Final plan approval binds
both the grant and exact-use digests. Cargo movement is allowed only inside the
approved cargo scope; in-transit cargo additionally requires an independently
verified physical transfer fact.

## Revision comparison

The authoritative artifact is `delivery.revision-comparison.v1`. It binds:

- base and candidate revision and plan digests;
- fact frontier and applied fact digests;
- policy and override digests;
- base and candidate effect-set digests.

It contains complete, canonical leaves for:

- route: unit/vehicle assignment, trips, stops, and legs;
- schedule: driver assignment, ETA, stop times, and duty segments;
- energy: SOC, consumption, charging, and charger changes;
- load: full placement, stage, axle, center-of-mass, and rehandle changes;
- effects: added, removed, and modified action/target/required/parameter digest;
- every integer field in `PlanMetrics`.

Every executable leaf has `ChangeMeta`:

```go
type ChangeMeta struct {
	ChangeID    string
	Facts       []domain.FactRef
	Policy      []PolicyRef
	OverrideIDs []domain.ApprovalID
	Mode        AttributionMode
}
```

Direct changes join fact applications by object and field path. Derived
changes cite the complete triggering fact set and exact policy values used by
the solver or Validator. An unattributed executable change is a build error.
The implementation does not invent a single cause for a global optimization
tradeoff.

A closed metric registry and contract test fail when `PlanMetrics` gains an
unhandled field.

Agent-3 receives a lossy projection with exactly:

```text
vehicle_assignment
driver_assignment
stop_sequence
eta
cargo_placement
metric
```

Rich energy, load-stage, schedule-segment, and effect leaves remain available
from the authoritative artifact.

## Transaction boundaries

The implementation extends Agent-2's existing `service.Store`. It must not
introduce a second repository or active-plan pointer.

### Record facts

`RecordOperationalFacts` claims command idempotency, writes canonical facts,
promotes contiguous heads, records conflicts, appends audit/outbox events, and
returns one durable receipt in a short transaction.

### Commit successor

`CommitReoptimization`:

1. claims command idempotency;
2. locks the plan and checks active revision/version;
3. locks the problem head and relevant stream heads in canonical order;
4. rechecks the exact applied and target frontiers and fact digests;
5. rechecks and consumes the optional override approval;
6. inserts the successor `ProblemVersion`, artifact references, and
   `OptimizationRun`;
7. records the run's target plan/base revision/active version;
8. advances the applied frontier;
9. appends audit/outbox events and the replay result.

Artifact writes and verification happen before this transaction. A lost CAS
may leave an unreachable content-addressed artifact, never a half-written
database reference.

### Publish successor revision

The existing `PublishRevision` operation is generalized:

- an initial run creates the plan and first revision;
- a successor run inserts under the existing plan and exact base revision;
- publication rechecks the active tuple and run fencing token;
- a stale run is retained as evidence but cannot become approvable.

### Reserve before effects

`DecideAndCreateExecution` inserts an execution reservation under the same
transaction that confirms approval and creates effects:

```text
UNIQUE active reservation per (tenant_id, plan_id)
binding: base_revision_id + base_active_version + execution_id
states: reserved | committed | reconciliation_required | released
```

The effect worker may claim work only for the current reservation owner and
only while the plan still has the bound active tuple. Partial or unknown
effects retain the reservation until reconciliation or audited compensation;
another candidate cannot dispatch concurrently.

`ActivateRevision` verifies the reservation, performs the final active tuple
CAS, supersedes the base revision, activates the candidate, commits the
execution, and marks the reservation committed in one transaction.

## Module map

```text
internal/delivery/domain/reoptimization.go
  fact streams, positions, frontier, ledger references, override artifacts

internal/delivery/service/reoptimization.go
  public commands, outcomes, pure successor construction

internal/delivery/service/reoptimization_diff.go
  complete comparison, attribution, Agent-3 projection

internal/delivery/service/platform.go
  additive operation-level Store and Commands contracts

internal/storage/postgres/delivery_reoptimization.go
  ledger, successor commit, override verification on DeliveryStore

internal/storage/postgres/delivery_store_execution.go
  pre-effect reservation integrated with approval/execution

internal/delivery/source/guardian.go
  authenticated CloudEvents anti-corruption mapping
```

The service module owns domain policy even when reduction and comparison happen
at different times. PostgreSQL owns locking and atomic state transitions, not
fact meaning.

## Synthesis decision

Candidate 1 is the base because it provides the deepest public command, the
clearest reuse of Agent-2's lifecycle, and the most complete recovery and
frontend projection story.

Grafted from Candidate 2:

- bounded pre-solve override grants bound to problem, policy, frontier, and
  expiry, plus exact post-solve use evidence;
- private `VerifiedFreezeOverride`;
- durable conflict records and audited resolution;
- normalized semantic indexes and mandatory attribution;
- policy version and value digest in attribution.

Grafted from Candidate 3:

- current service stop, completed duty prefix, paired delivery/return work, and
  current cargo placement in the protected closure;
- explicit cargo override scope;
- a closed metric registry guarded by contract tests.

All candidates missed the pre-effect race. The synthesis adds an execution
reservation CAS at approval confirmation, before effects become claimable,
while retaining the final activation CAS.

## Tradeoffs accepted

- We accept retained fact and conflict rows in exchange for durable replay,
  gap detection, and forensic evidence.
- We accept two short CAS stages in exchange for preventing stale external
  effects as well as stale activation.
- We accept orphaned content-addressed artifacts after a lost CAS in exchange
  for keeping object I/O outside database transactions.
- We accept a separate override approval in exchange for proving that frozen
  constraints were relaxed independently and exactly.
- We accept a rich backend comparison plus a narrower UI projection in
  exchange for complete evidence and strict frontend compatibility.

## Alternatives considered

**One global watermark.** Rejected because independent streams have no
trustworthy total offset and gaps cannot be attributed.

**Batch-local replay.** Rejected because callers would own cross-batch
deduplication, gap handling, and crash windows.

**A separate reoptimization repository.** Rejected because it duplicates
Agent-2's plan, run, approval, audit, and active pointer.

**Final plan approval as freeze authorization.** Rejected because the solver
would have relaxed constraints before independent approval existed.

**Activation CAS only.** Rejected because two candidates could both perform
external effects before one loses activation.

## Open questions and risks

- Upstream adapters that lack stable sequence numbers need an authenticated
  inbox sequencer; its assigned stream must not be confused with producer
  order.
- Stream epoch resets require a separate administrative protocol and audit.
- Cancellation after pickup needs explicit deliver, return, transfer, or safe
  unload facts; the reducer cannot invent a disposition.
- Releasing an execution reservation after partial effects requires verified
  compensation or reconciliation, never a timeout alone.

## Next implementation step

Implement the v2 fact/frontier contracts and pure ledger transition tests,
then replace the batch-local reducer with exact frontier reduction and the
protected execution closure.
