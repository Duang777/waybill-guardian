package audit

import (
	"encoding/json"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
)

type Seq uint64
type Actor string
type EventType string

const (
	ActorAgent  Actor = "agent"
	ActorHuman  Actor = "human"
	ActorSystem Actor = "system"

	EventRunStarted          EventType = "run_started"
	EventToolCall            EventType = "tool_call"
	EventToolResult          EventType = "tool_result"
	EventAttribution         EventType = "attribution"
	EventApprovalRequested   EventType = "approval_requested"
	EventApprovalDecided     EventType = "approval_decided"
	EventApprovalExecuted    EventType = "approval_executed"
	EventWriteStarted        EventType = "write_started"
	EventWriteExecuted       EventType = "write_executed"
	EventWriteFailed         EventType = "write_failed"
	EventDuplicateSuppressed EventType = "duplicate_suppressed"
	EventRunCompleted        EventType = "run_completed"
	EventRunRejected         EventType = "run_rejected"
	EventRunFailed           EventType = "run_failed"
	EventNote                EventType = "note"
)

const GenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

type Event struct {
	SchemaVersion int             `json:"schema_version"`
	EventID       string          `json:"event_id"`
	Seq           Seq             `json:"seq"`
	TS            time.Time       `json:"ts"`
	RunID         domain.RunID    `json:"run_id"`
	Actor         Actor           `json:"actor"`
	Type          EventType       `json:"type"`
	Payload       json.RawMessage `json:"payload"`
	PrevHash      string          `json:"prev_hash"`
	Hash          string          `json:"hash"`
}

type Draft struct {
	EventID string
	Actor   Actor
	Type    EventType
	Payload any
}
