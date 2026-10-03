package approval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
)

var (
	ErrNotFound           = errors.New("approval not found")
	ErrDecisionConflict   = errors.New("approval decision conflicts with current status")
	ErrRejectReason       = errors.New("reject reason is required")
	ErrApprovalNotGranted = errors.New("write call is not covered by a confirmed approval")
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusConfirmed Status = "confirmed"
	StatusExecuted  Status = "executed"
	StatusRejected  Status = "rejected"
	StatusExpired   Status = "expired"
)

type DecisionKind string

const (
	DecisionConfirm DecisionKind = "confirm"
	DecisionReject  DecisionKind = "reject"
	DecisionExpire  DecisionKind = "expire"
)

type Evidence struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type Item struct {
	CallID         string                `json:"call_id"`
	Action         domain.Action         `json:"action"`
	WireName       string                `json:"wire_name"`
	Params         json.RawMessage       `json:"params"`
	ArgumentsHash  string                `json:"arguments_hash"`
	IdempotencyKey domain.IdempotencyKey `json:"idempotency_key"`
}

type Approval struct {
	ID           domain.ApprovalID `json:"id"`
	RunID        domain.RunID      `json:"run_id"`
	SDKRunID     string            `json:"sdk_run_id"`
	WaybillID    domain.WaybillID  `json:"waybill_id"`
	PlanVersion  int               `json:"plan_version"`
	Items        []Item            `json:"items"`
	Reason       string            `json:"reason"`
	Evidence     []Evidence        `json:"evidence"`
	Status       Status            `json:"status"`
	RequestedAt  time.Time         `json:"requested_at"`
	ExpiresAt    time.Time         `json:"expires_at"`
	DecidedBy    string            `json:"decided_by,omitempty"`
	DecidedAt    *time.Time        `json:"decided_at,omitempty"`
	RejectReason string            `json:"reject_reason,omitempty"`
}

type Decision struct {
	Kind         DecisionKind
	DecidedBy    string
	RejectReason string
}

type Store struct {
	journal *audit.Store
	clock   func() time.Time

	mu        sync.Mutex
	approvals map[domain.ApprovalID]Approval
}

type decisionPayload struct {
	ApprovalID   domain.ApprovalID `json:"approval_id"`
	Status       Status            `json:"status"`
	DecidedBy    string            `json:"decided_by"`
	DecidedAt    time.Time         `json:"decided_at"`
	RejectReason string            `json:"reject_reason,omitempty"`
}

type executedPayload struct {
	ApprovalID domain.ApprovalID `json:"approval_id"`
	ExecutedAt time.Time         `json:"executed_at"`
}

func NewStore(journal *audit.Store, clock func() time.Time) (*Store, error) {
	if journal == nil {
		return nil, fmt.Errorf("audit journal is required")
	}
	if clock == nil {
		clock = time.Now
	}
	store := &Store{
		journal:   journal,
		clock:     clock,
		approvals: make(map[domain.ApprovalID]Approval),
	}
	for _, event := range journal.AllEvents() {
		if err := store.apply(event); err != nil {
			return nil, fmt.Errorf("rebuild approvals: %w", err)
		}
	}
	return store, nil
}

func IDFor(runID domain.RunID, callIDs []string) domain.ApprovalID {
	sorted := append([]string(nil), callIDs...)
	sort.Strings(sorted)
	hash := sha256.New()
	_, _ = hash.Write([]byte(runID))
	for _, callID := range sorted {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(callID))
	}
	return domain.ApprovalID("APR-" + hex.EncodeToString(hash.Sum(nil)[:8]))
}

func (s *Store) Create(ctx context.Context, value Approval) (Approval, error) {
	if value.ID == "" || value.RunID == "" || value.WaybillID == "" || len(value.Items) == 0 {
		return Approval{}, fmt.Errorf("approval id, run id, waybill id, and items are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.approvals[value.ID]; ok {
		return existing, nil
	}
	now := s.clock().UTC()
	value.Status = StatusPending
	value.RequestedAt = now
	if value.ExpiresAt.IsZero() {
		value.ExpiresAt = now.Add(10 * time.Minute)
	} else {
		value.ExpiresAt = value.ExpiresAt.UTC()
	}
	event, err := s.journal.Append(ctx, value.RunID, audit.Draft{
		EventID: "approval:" + string(value.ID) + ":requested",
		Actor:   audit.ActorAgent,
		Type:    audit.EventApprovalRequested,
		Payload: value,
	})
	if err != nil {
		return Approval{}, err
	}
	var stored Approval
	if err := json.Unmarshal(event.Payload, &stored); err != nil {
		return Approval{}, fmt.Errorf("decode stored approval: %w", err)
	}
	s.approvals[value.ID] = stored
	return clone(stored), nil
}

func (s *Store) Decide(ctx context.Context, id domain.ApprovalID, decision Decision) (Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.approvals[id]
	if !ok {
		return Approval{}, ErrNotFound
	}
	next, err := nextStatus(current.Status, decision.Kind)
	if err != nil {
		if matchesDecision(current.Status, decision.Kind) {
			return clone(current), nil
		}
		return Approval{}, err
	}
	if decision.Kind == DecisionReject && decision.RejectReason == "" {
		return Approval{}, ErrRejectReason
	}
	now := s.clock().UTC()
	payload := decisionPayload{
		ApprovalID:   id,
		Status:       next,
		DecidedBy:    decision.DecidedBy,
		DecidedAt:    now,
		RejectReason: decision.RejectReason,
	}
	actor := audit.ActorHuman
	if decision.Kind == DecisionExpire {
		actor = audit.ActorSystem
		payload.DecidedBy = "system"
	}
	event, err := s.journal.Append(ctx, current.RunID, audit.Draft{
		EventID: "approval:" + string(id) + ":" + string(next),
		Actor:   actor,
		Type:    audit.EventApprovalDecided,
		Payload: payload,
	})
	if err != nil {
		return Approval{}, err
	}
	if err := s.applyLocked(event); err != nil {
		return Approval{}, err
	}
	return clone(s.approvals[id]), nil
}

func (s *Store) MarkExecuted(ctx context.Context, id domain.ApprovalID) (Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.approvals[id]
	if !ok {
		return Approval{}, ErrNotFound
	}
	if current.Status == StatusExecuted {
		return clone(current), nil
	}
	if current.Status != StatusConfirmed {
		return Approval{}, ErrDecisionConflict
	}
	event, err := s.journal.Append(ctx, current.RunID, audit.Draft{
		EventID: "approval:" + string(id) + ":executed",
		Actor:   audit.ActorSystem,
		Type:    audit.EventApprovalExecuted,
		Payload: executedPayload{ApprovalID: id, ExecutedAt: s.clock().UTC()},
	})
	if err != nil {
		return Approval{}, err
	}
	if err := s.applyLocked(event); err != nil {
		return Approval{}, err
	}
	return clone(s.approvals[id]), nil
}

func (s *Store) Get(id domain.ApprovalID) (Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.approvals[id]
	if !ok {
		return Approval{}, ErrNotFound
	}
	return clone(value), nil
}

func (s *Store) List() []Approval {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Approval, 0, len(s.approvals))
	for _, value := range s.approvals {
		result = append(result, clone(value))
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].RequestedAt.Before(result[j].RequestedAt)
	})
	return result
}

func (s *Store) Allows(runID domain.RunID, callID, argumentsHash string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, value := range s.approvals {
		if value.RunID != runID || (value.Status != StatusConfirmed && value.Status != StatusExecuted) {
			continue
		}
		for _, item := range value.Items {
			if item.CallID == callID && item.ArgumentsHash == argumentsHash {
				return true
			}
		}
	}
	return false
}

func (s *Store) ExpireDue(ctx context.Context) ([]Approval, error) {
	now := s.clock().UTC()
	s.mu.Lock()
	var ids []domain.ApprovalID
	for id, value := range s.approvals {
		if value.Status == StatusPending && !value.ExpiresAt.After(now) {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()

	expired := make([]Approval, 0, len(ids))
	for _, id := range ids {
		value, err := s.Decide(ctx, id, Decision{Kind: DecisionExpire, DecidedBy: "system"})
		if err != nil {
			return nil, err
		}
		expired = append(expired, value)
	}
	return expired, nil
}

func (s *Store) apply(event audit.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyLocked(event)
}

func (s *Store) applyLocked(event audit.Event) error {
	switch event.Type {
	case audit.EventApprovalRequested:
		var value Approval
		if err := json.Unmarshal(event.Payload, &value); err != nil {
			return err
		}
		s.approvals[value.ID] = value
	case audit.EventApprovalDecided:
		var payload decisionPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		current, ok := s.approvals[payload.ApprovalID]
		if !ok {
			return fmt.Errorf("decision references unknown approval %q", payload.ApprovalID)
		}
		current.Status = payload.Status
		current.DecidedBy = payload.DecidedBy
		current.DecidedAt = &payload.DecidedAt
		current.RejectReason = payload.RejectReason
		s.approvals[current.ID] = current
	case audit.EventApprovalExecuted:
		var payload executedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		current, ok := s.approvals[payload.ApprovalID]
		if !ok {
			return fmt.Errorf("execution references unknown approval %q", payload.ApprovalID)
		}
		current.Status = StatusExecuted
		s.approvals[current.ID] = current
	}
	return nil
}

func nextStatus(current Status, decision DecisionKind) (Status, error) {
	if current != StatusPending {
		return "", ErrDecisionConflict
	}
	switch decision {
	case DecisionConfirm:
		return StatusConfirmed, nil
	case DecisionReject:
		return StatusRejected, nil
	case DecisionExpire:
		return StatusExpired, nil
	default:
		return "", fmt.Errorf("unknown decision %q", decision)
	}
}

func matchesDecision(status Status, decision DecisionKind) bool {
	return (status == StatusConfirmed || status == StatusExecuted) && decision == DecisionConfirm ||
		status == StatusRejected && decision == DecisionReject ||
		status == StatusExpired && decision == DecisionExpire
}

func clone(value Approval) Approval {
	value.Items = append([]Item(nil), value.Items...)
	for index := range value.Items {
		value.Items[index].Params = append(json.RawMessage(nil), value.Items[index].Params...)
	}
	value.Evidence = append([]Evidence(nil), value.Evidence...)
	return value
}
