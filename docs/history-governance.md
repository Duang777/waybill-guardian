# Agent history governance

## Problem

Hastekit sends tool results to the next model request and persists the same
messages. At an approval pause, it also stores executable tool calls in
`run_state.pending_interrupts`. The current read tools expose phone numbers,
license plates, and exact coordinates. The SMS tool asks the model to provide a
phone number, provider template, and full parameter map.

Persistence-only masking is not valid. It runs after read results have reached
the model, and changing a paused call invalidates the approval argument hash.
Encryption protects storage media but does not minimize model input.

## Usage

The model receives allowlisted operational evidence:

```json
{
  "driver_id": "DRV-0286",
  "continuous_drive_hours": 9,
  "fatigue_alert": true
}
```

Tracking evidence keeps timestamps, speed, stop duration, and anomaly state,
but omits longitude and latitude.

The model requests a notification by business role:

```json
{
  "waybill_id": "YD2026101001",
  "recipient": "shipper",
  "carrier_id": "CARRIER-SW-42"
}
```

The same arguments enter the approval, effect identity, pause checkpoint, and
resume path. After approval, the tool handler resolves the current contact,
selects the fixed template, builds parameters, and calls the notification
adapter.

Both engine constructors install the same model and persistence guards:

```go
guard := agent.NewHistoryGuard()
engine, err := agent.NewEngineWithPersistence(
	guard.Wrap(persistence),
	registry,
	append([]agents.Middleware{guard.ModelMiddleware()}, middlewares...),
	stepDelay,
	modelConfig,
)
```

The constructors own this wiring. Callers cannot accidentally pass an
unguarded history adapter or omit the model check.

## Shape

### Safe tool contracts

`internal/tools` defines model-specific result types instead of aliasing
`internal/platform` records:

```go
type WaybillEvidence struct {
	WaybillID         domain.WaybillID `json:"waybill_id"`
	Origin            string           `json:"origin"`
	Destination       string           `json:"destination"`
	Cargo             string           `json:"cargo"`
	CarrierID         domain.CarrierID `json:"carrier_id"`
	DriverID          domain.DriverID  `json:"driver_id"`
	Status            string           `json:"status"`
	SLAHours          int              `json:"sla_hours"`
	CandidateCarriers []CarrierEvidence `json:"candidate_carriers"`
}

type TrackingEvidence struct {
	Label      string  `json:"label"`
	RecordedAt string  `json:"recorded_at"`
	SpeedKPH   int     `json:"speed_kph"`
	StopHours  float64 `json:"stop_hours,omitempty"`
	Anomaly    bool    `json:"anomaly"`
}

type DriverEvidence struct {
	DriverID           domain.DriverID `json:"driver_id"`
	ContinuousDriveHrs float64         `json:"continuous_drive_hours"`
	FatigueAlert       bool            `json:"fatigue_alert"`
}
```

Handlers construct these values field by field. A new platform field cannot
cross the model boundary without an explicit tool contract change.

The notification tool accepts an intent:

```go
type NotificationRecipient string

const (
	RecipientShipper NotificationRecipient = "shipper"
	RecipientDriver  NotificationRecipient = "driver"
)

type SendSMSInput struct {
	WaybillID string                `json:"waybill_id"`
	Recipient NotificationRecipient `json:"recipient"`
	CarrierID string                `json:"carrier_id"`
}
```

`Registry.ParseWrite` derives the effect target from the waybill and recipient.
The handler resolves the real destination only after the approval and
idempotency middleware accept the call. This keeps restart recovery on one
safe executable representation.

### Shared guard

`internal/agent` owns one immutable history policy. It checks:

- keys named `phone`, `shipper_phone`, `plate`, `license_plate`, `longitude`,
  and `latitude`;
- legacy SMS keys `template_id` and `params`;
- Chinese mobile numbers and license plates in scalar strings;
- JSON nested inside function arguments and tool-output strings.

The model middleware checks the exact request before it reaches a provider and
checks the response before hastekit adds it to the run. The persistence wrapper
checks messages, metadata, pending interrupts, and summaries before delegating
to either storage implementation. It stamps copied metadata with the current
history schema version and rejects unknown versions on load.

The guard validates executable calls instead of rewriting them. A prohibited
call fails before storage or execution.

### File history

The project keeps hastekit's tested file adapter. `secureHistoryPersistence`
adds one sidecar per conversation:

```go
type historyMetadata struct {
	SchemaVersion  uint16     `json:"schema_version"`
	ConversationID string     `json:"conversation_id"`
	ThreadID       string     `json:"thread_id"`
	TerminalAt     *time.Time `json:"terminal_at,omitempty"`
}
```

The adapter creates the sidecar before the first JSONL append and updates it
after a terminal save. Files without a current sidecar are legacy. Startup
removes legacy JSONL before hastekit replays it. Current active histories are
never age-pruned. Current terminal histories older than
`HISTORY_RETENTION` are removed during startup.

The one-time cleanup can make an old local paused run non-resumable.
`guardian.Recover` records that run as failed while preserving its business
audit log. The application does not translate legacy SMS calls because that
would change the approved operation.

### PostgreSQL history

Migration `000004_history_governance.sql`:

1. quarantines active runs that reference pre-policy checkpoints;
2. deletes existing checkpoints and summaries;
3. drops the plaintext `agent_checkpoints.metadata` copy;
4. adds a constrained `privacy_schema_version` column.

New checkpoint and summary payloads remain protected by AES-256-GCM after the
shared guard accepts them. The version column contains only a format number.
The adapter rejects an unsupported version before decryption output reaches
hastekit.

At startup, PostgreSQL deletes checkpoint and summary rows only for terminal
runs whose `closed_at` is older than the retention cutoff. The deletion
transaction repeats the terminal-state predicate. Audit events, approvals,
effects, and run projections are not part of this deletion.

### Retention and deletion

`HISTORY_RETENTION` defaults to seven days and must be positive. Each server
startup runs one retention pass before recovery. Operators can force complete
history deletion by stopping the service and using the documented backend
procedure:

- local mode: remove only `DATA_DIR/hastekit`;
- PostgreSQL mode: delete `agent_summaries` and `agent_checkpoints` for the
  selected tenant in one transaction, then clear the corresponding terminal
  run checkpoint pointers.

Conversation history is a recoverability cache. The append-only audit record
has a separate retention policy and remains intact.

## Synthesis decision

Candidate 2 is the base. It scored 28/30 in the independent review because it
removes sensitive values before hastekit, keeps pause/resume on one executable
representation, and reuses the SDK file adapter.

Candidate 1's file-backend rewrite was rejected because it would duplicate SDK
branching, merge, replay, and summary behavior. Candidate 3's public lifecycle
methods were rejected because they made `guardian` coordinate preparation,
quarantine, terminal marking, and pruning.

The final design takes the per-conversation sidecar marker from candidate 3.
It also takes candidate 1's tests that prove prohibited values are rejected
before backend writes and that summaries use the same policy.

## Tradeoffs accepted

- We accept a breaking SMS tool schema in exchange for removing transport
  details and contact data from model authority.
- We accept contact lookup at first dispatch in exchange for retaining no
  destination snapshot in model history.
- We accept deleting legacy history instead of translating it because
  translation would change approved argument hashes.
- We accept startup-based retention for the local demo adapter. Production
  deployment must run the same startup sweep on its normal restart cadence.
- We retain encryption after minimization as defense in depth.

## Alternatives considered

- Opaque contact tokens require a durable token map, expiry, and recovery
  protocol. The model would need to coordinate token acquisition.
- A second encrypted pending-call store duplicates call identity and creates an
  atomicity requirement between transcript, approval, and recovery state.
- Persistence-only redaction leaves model input exposed and breaks resumed
  executable calls.

## Verification contract

1. Tool JSON contains operational evidence but no fixture phone, plate, exact
   coordinate, provider template, or template parameter map.
2. A complete model request passes the shared guard; injected prohibited
   values fail before the wrapped model runs.
3. A spy persistence adapter proves prohibited messages, metadata, and
   summaries fail before a backend write.
4. A paused file-backed run contains no prohibited fixture value, survives
   restart, and executes one reassign plus two notifications after approval.
5. The notification adapter receives the real contacts and derived provider
   parameters only after approval.
6. PostgreSQL has no plaintext metadata column. Decrypted payloads pass the
   shared guard, and cross-instance resume still works.
7. Legacy cleanup and retention are idempotent. They remove legacy or expired
   terminal history, retain active current-format history, and leave audit
   records unchanged.
8. A rerunnable repository script scans generated file history and PostgreSQL
   plaintext columns for prohibited fixture values.

## Next implementation step

Implement and test the allowlisted read outputs and role-based notification
intent before changing either persistence implementation.
