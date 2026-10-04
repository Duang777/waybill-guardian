package approval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/idempotency"
)

var (
	ErrNotFound                = errors.New("approval not found")
	ErrDecisionConflict        = errors.New("approval decision conflicts with current status")
	ErrRejectReason            = errors.New("reject reason is required")
	ErrApprovalNotGranted      = errors.New("write call is not covered by a confirmed approval")
	ErrDuplicateCallID         = errors.New("approval contains duplicate call_id")
	ErrDuplicateEffect         = errors.New("approval contains duplicate effect_id")
	ErrInvalidEffectIdentity   = errors.New("approval contains an invalid effect identity")
	ErrAmbiguousLegacyApproval = errors.New("legacy approval contains unresolved items with the same key")
)

type Status string

const (
	StatusPending                Status = "pending"
	StatusConfirmed              Status = "confirmed"
	StatusReconciliationRequired Status = "reconciliation_required"
	StatusExecuted               Status = "executed"
	StatusPartiallyFailed        Status = "partially_failed"
	StatusFailed                 Status = "failed"
	StatusRejected               Status = "rejected"
	StatusExpired                Status = "expired"
)

type DecisionKind string

const (
	DecisionConfirm DecisionKind = "confirm"
	DecisionReject  DecisionKind = "reject"
	DecisionExpire  DecisionKind = "expire"
)

type Evidence struct {
	Label  string          `json:"label"`
	Value  string          `json:"value"`
	Source *EvidenceSource `json:"source,omitempty"`
}

type EvidenceSource struct {
	ToolCallID string    `json:"tool_call_id"`
	FieldPath  string    `json:"field_path"`
	SourceSeq  audit.Seq `json:"source_seq"`
}

type ProposalRef struct {
	ProposalID string `json:"proposal_id"`
	EventID    string `json:"event_id"`
	Digest     string `json:"digest"`
}

type Item struct {
	CallID          string                      `json:"call_id"`
	Action          domain.Action               `json:"action"`
	WireName        string                      `json:"wire_name"`
	Params          json.RawMessage             `json:"params"`
	ArgumentsHash   string                      `json:"arguments_hash"`
	IdentityVersion idempotency.IdentityVersion `json:"identity_version,omitempty"`
	EffectID        domain.EffectID             `json:"effect_id,omitempty"`
	IdempotencyKey  domain.IdempotencyKey       `json:"idempotency_key"`
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
	ProposalRef  *ProposalRef      `json:"proposal_ref,omitempty"`
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

type ExecutionStatus string

const (
	ExecutionSucceeded     ExecutionStatus = "succeeded"
	ExecutionFailed        ExecutionStatus = "failed"
	ExecutionStarted       ExecutionStatus = "started"
	ExecutionIndeterminate ExecutionStatus = "indeterminate"
	ExecutionRetryable     ExecutionStatus = "retryable_failed"
	ExecutionPermanent     ExecutionStatus = "permanent_failed"
	ExecutionUnknown       ExecutionStatus = "unknown"
	ExecutionReconciling   ExecutionStatus = "reconciling"
	ExecutionManualReview  ExecutionStatus = "manual_review"
	ExecutionMissing       ExecutionStatus = "missing"
)

type ItemExecution struct {
	CallID         string                `json:"call_id"`
	Action         domain.Action         `json:"action"`
	EffectID       domain.EffectID       `json:"effect_id,omitempty"`
	IdempotencyKey domain.IdempotencyKey `json:"idempotency_key"`
	Status         ExecutionStatus       `json:"status"`
}

type AuthorizationRequest struct {
	RunID                 domain.RunID
	CallID                string
	Action                domain.Action
	WireName              string
	BusinessArgumentsHash string
	LegacyArgumentsHash   string
	LegacyKey             domain.IdempotencyKey
}

type Authorization struct {
	Item            Item
	LegacyAmbiguous bool
}

func (item Item) Identity() (idempotency.Identity, error) {
	if item.IdentityVersion == "" && item.EffectID == "" {
		return idempotency.LegacyIdentity(item.Action, item.IdempotencyKey, item.ArgumentsHash)
	}
	version := item.IdentityVersion
	if version == "" {
		version = idempotency.IdentityEffectV0
	}
	identity := idempotency.Identity{
		Version:       version,
		EffectID:      item.EffectID,
		Key:           item.IdempotencyKey,
		Action:        item.Action,
		ArgumentsHash: item.ArgumentsHash,
	}
	if err := identity.Validate(); err != nil {
		return idempotency.Identity{}, errors.Join(ErrInvalidEffectIdentity, err)
	}
	return identity, nil
}

type Store struct {
	journal audit.Journal
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

type executionFailedPayload struct {
	ApprovalID domain.ApprovalID `json:"approval_id"`
	Status     Status            `json:"status"`
	FailedAt   time.Time         `json:"failed_at"`
	Items      []ItemExecution   `json:"items"`
}

type reconciliationRequiredPayload struct {
	ApprovalID domain.ApprovalID `json:"approval_id"`
	CheckedAt  time.Time         `json:"checked_at"`
	Items      []ItemExecution   `json:"items"`
}

func NewStore(journal audit.Journal, clock func() time.Time) (*Store, error) {
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
	events, err := journal.AllEvents(context.Background())
	if err != nil {
		return nil, fmt.Errorf("read approval events: %w", err)
	}
	for _, event := range events {
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

func IDForPlan(
	runID domain.RunID,
	planVersion int,
	callIDs []string,
) domain.ApprovalID {
	sorted := append([]string(nil), callIDs...)
	sort.Strings(sorted)
	hash := sha256.New()
	_, _ = hash.Write([]byte("waybill-approval-v2"))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(runID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strconv.Itoa(planVersion)))
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
	if err := validateItems(value.Items); err != nil {
		return Approval{}, err
	}
	if err := validateProposal(value); err != nil {
		return Approval{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.approvals[value.ID]; ok {
		return existing, nil
	}
	now := s.clock().UTC()
	value.Status = StatusPending
	if value.RequestedAt.IsZero() {
		value.RequestedAt = now
	} else {
		value.RequestedAt = value.RequestedAt.UTC()
	}
	if value.ExpiresAt.IsZero() {
		value.ExpiresAt = value.RequestedAt.Add(10 * time.Minute)
	} else {
		value.ExpiresAt = value.ExpiresAt.UTC()
	}
	if !value.ExpiresAt.After(value.RequestedAt) {
		return Approval{}, errors.New("approval expires_at must be after requested_at")
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
	now := s.clock().UTC()
	expiredBeforeDecision := current.Status == StatusPending &&
		(decision.Kind == DecisionConfirm || decision.Kind == DecisionReject) &&
		!current.ExpiresAt.After(now)
	next := StatusExpired
	if !expiredBeforeDecision {
		var err error
		next, err = nextStatus(current.Status, decision.Kind)
		if err != nil {
			if matchesDecision(current.Status, decision.Kind) {
				return clone(current), nil
			}
			return Approval{}, err
		}
	}
	if decision.Kind == DecisionReject && !expiredBeforeDecision && decision.RejectReason == "" {
		return Approval{}, ErrRejectReason
	}
	payload := decisionPayload{
		ApprovalID:   id,
		Status:       next,
		DecidedBy:    decision.DecidedBy,
		DecidedAt:    now,
		RejectReason: decision.RejectReason,
	}
	actor := audit.ActorHuman
	if decision.Kind == DecisionExpire || expiredBeforeDecision {
		actor = audit.ActorSystem
		payload.DecidedBy = "system"
		payload.RejectReason = ""
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
	result := clone(s.approvals[id])
	if expiredBeforeDecision {
		return result, ErrDecisionConflict
	}
	return result, nil
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
	if current.Status != StatusConfirmed && current.Status != StatusReconciliationRequired {
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

func (s *Store) MarkExecutionFailed(
	ctx context.Context,
	id domain.ApprovalID,
	items []ItemExecution,
) (Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.approvals[id]
	if !ok {
		return Approval{}, ErrNotFound
	}
	if current.Status == StatusPartiallyFailed || current.Status == StatusFailed {
		return clone(current), nil
	}
	if current.Status != StatusConfirmed && current.Status != StatusReconciliationRequired {
		return Approval{}, ErrDecisionConflict
	}
	status, err := validateExecutionFailure(current.Items, items)
	if err != nil {
		return Approval{}, err
	}
	event, err := s.journal.Append(ctx, current.RunID, audit.Draft{
		EventID: "approval:" + string(id) + ":execution_failed",
		Actor:   audit.ActorSystem,
		Type:    audit.EventApprovalExecutionFailed,
		Payload: executionFailedPayload{
			ApprovalID: id,
			Status:     status,
			FailedAt:   s.clock().UTC(),
			Items:      append([]ItemExecution(nil), items...),
		},
	})
	if err != nil {
		return Approval{}, err
	}
	if err := s.applyLocked(event); err != nil {
		return Approval{}, err
	}
	return clone(s.approvals[id]), nil
}

func (s *Store) MarkReconciliationRequired(
	ctx context.Context,
	id domain.ApprovalID,
	items []ItemExecution,
) (Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.approvals[id]
	if !ok {
		return Approval{}, ErrNotFound
	}
	if current.Status == StatusReconciliationRequired {
		return clone(current), nil
	}
	if current.Status != StatusConfirmed {
		return Approval{}, ErrDecisionConflict
	}
	if err := validateReconciliationRequired(current.Items, items); err != nil {
		return Approval{}, err
	}
	event, err := s.journal.Append(ctx, current.RunID, audit.Draft{
		EventID: "approval:" + string(id) + ":reconciliation_required",
		Actor:   audit.ActorSystem,
		Type:    audit.EventApprovalReconciliationRequired,
		Payload: reconciliationRequiredPayload{
			ApprovalID: id,
			CheckedAt:  s.clock().UTC(),
			Items:      append([]ItemExecution(nil), items...),
		},
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

func (s *Store) Authorize(request AuthorizationRequest) (Authorization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, value := range s.approvals {
		if value.RunID != request.RunID ||
			(value.Status != StatusConfirmed &&
				value.Status != StatusReconciliationRequired &&
				value.Status != StatusExecuted) {
			continue
		}
		for _, item := range value.Items {
			if item.CallID != request.CallID ||
				item.Action != request.Action ||
				item.WireName != request.WireName {
				continue
			}
			if item.IdentityVersion == "" && item.EffectID == "" {
				if request.LegacyKey != item.IdempotencyKey ||
					request.LegacyArgumentsHash != item.ArgumentsHash {
					return Authorization{}, ErrApprovalNotGranted
				}
				return Authorization{
					Item:            cloneItem(item),
					LegacyAmbiguous: legacyKeyCount(value.Items, item.IdempotencyKey) > 1,
				}, nil
			}
			if request.LegacyKey != "" || request.BusinessArgumentsHash != item.ArgumentsHash {
				return Authorization{}, ErrApprovalNotGranted
			}
			return Authorization{Item: cloneItem(item)}, nil
		}
	}
	return Authorization{}, ErrApprovalNotGranted
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
		if err := validateItems(value.Items); err != nil {
			return err
		}
		if err := validateProposal(value); err != nil {
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
	case audit.EventApprovalExecutionFailed:
		var payload executionFailedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		current, ok := s.approvals[payload.ApprovalID]
		if !ok {
			return fmt.Errorf("execution failure references unknown approval %q", payload.ApprovalID)
		}
		status, err := validateExecutionFailure(current.Items, payload.Items)
		if err != nil {
			return fmt.Errorf("invalid execution failure for approval %q: %w", payload.ApprovalID, err)
		}
		if payload.Status != status {
			return fmt.Errorf(
				"execution failure status %q does not match item status %q",
				payload.Status,
				status,
			)
		}
		current.Status = status
		s.approvals[current.ID] = current
	case audit.EventApprovalReconciliationRequired:
		var payload reconciliationRequiredPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		current, ok := s.approvals[payload.ApprovalID]
		if !ok {
			return fmt.Errorf("reconciliation references unknown approval %q", payload.ApprovalID)
		}
		if err := validateReconciliationRequired(current.Items, payload.Items); err != nil {
			return fmt.Errorf("invalid reconciliation for approval %q: %w", payload.ApprovalID, err)
		}
		current.Status = StatusReconciliationRequired
		s.approvals[current.ID] = current
	}
	return nil
}

func validateExecutionFailure(expected []Item, actual []ItemExecution) (Status, error) {
	if len(actual) != len(expected) {
		return "", fmt.Errorf("execution results = %d, want %d", len(actual), len(expected))
	}
	remaining := make(map[string]Item, len(expected))
	for _, item := range expected {
		remaining[item.CallID] = item
	}
	succeeded := 0
	for _, result := range actual {
		item, ok := remaining[result.CallID]
		if !ok ||
			item.Action != result.Action ||
			item.EffectID != result.EffectID ||
			item.IdempotencyKey != result.IdempotencyKey {
			return "", fmt.Errorf("execution result does not match approved call %q", result.CallID)
		}
		delete(remaining, result.CallID)
		switch result.Status {
		case ExecutionSucceeded:
			succeeded++
		case ExecutionFailed, ExecutionRetryable, ExecutionPermanent,
			ExecutionStarted, ExecutionIndeterminate, ExecutionMissing:
		default:
			return "", fmt.Errorf("execution failure contains non-failure status %q", result.Status)
		}
	}
	if succeeded == len(actual) {
		return "", fmt.Errorf("execution failure contains no failed items")
	}
	if succeeded > 0 {
		return StatusPartiallyFailed, nil
	}
	return StatusFailed, nil
}

func validateReconciliationRequired(expected []Item, actual []ItemExecution) error {
	if len(actual) != len(expected) {
		return fmt.Errorf("execution results = %d, want %d", len(actual), len(expected))
	}
	remaining := make(map[string]Item, len(expected))
	for _, item := range expected {
		remaining[item.CallID] = item
	}
	requiresReconciliation := false
	for _, result := range actual {
		item, ok := remaining[result.CallID]
		if !ok ||
			item.Action != result.Action ||
			item.EffectID != result.EffectID ||
			item.IdempotencyKey != result.IdempotencyKey {
			return fmt.Errorf("execution result does not match approved call %q", result.CallID)
		}
		delete(remaining, result.CallID)
		switch result.Status {
		case ExecutionUnknown, ExecutionReconciling, ExecutionStarted,
			ExecutionIndeterminate, ExecutionManualReview:
			requiresReconciliation = true
		case ExecutionSucceeded, ExecutionRetryable, ExecutionPermanent, ExecutionFailed, ExecutionMissing:
		default:
			return fmt.Errorf("unknown execution status %q", result.Status)
		}
	}
	if !requiresReconciliation {
		return fmt.Errorf("execution results contain no effect requiring reconciliation")
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
	return (status == StatusConfirmed || status == StatusReconciliationRequired || status == StatusExecuted) &&
		decision == DecisionConfirm ||
		status == StatusRejected && decision == DecisionReject ||
		status == StatusExpired && decision == DecisionExpire
}

func clone(value Approval) Approval {
	value.Items = append([]Item(nil), value.Items...)
	for index := range value.Items {
		value.Items[index] = cloneItem(value.Items[index])
	}
	value.Evidence = append([]Evidence(nil), value.Evidence...)
	for index := range value.Evidence {
		if value.Evidence[index].Source == nil {
			continue
		}
		source := *value.Evidence[index].Source
		value.Evidence[index].Source = &source
	}
	if value.ProposalRef != nil {
		proposalRef := *value.ProposalRef
		value.ProposalRef = &proposalRef
	}
	return value
}

func cloneItem(value Item) Item {
	value.Params = append(json.RawMessage(nil), value.Params...)
	return value
}

func validateItems(items []Item) error {
	callIDs := make(map[string]struct{}, len(items))
	effectIDs := make(map[domain.EffectID]struct{}, len(items))
	for _, item := range items {
		if item.CallID == "" ||
			!item.Action.IsWrite() ||
			item.WireName == "" ||
			len(item.Params) == 0 ||
			item.ArgumentsHash == "" ||
			item.IdempotencyKey == "" {
			return ErrInvalidEffectIdentity
		}
		if _, exists := callIDs[item.CallID]; exists {
			return ErrDuplicateCallID
		}
		callIDs[item.CallID] = struct{}{}

		if item.IdentityVersion == "" && item.EffectID == "" {
			continue
		}
		version := item.IdentityVersion
		if version == "" {
			version = idempotency.IdentityEffectV0
		}
		identity := idempotency.Identity{
			Version:       version,
			EffectID:      item.EffectID,
			Key:           item.IdempotencyKey,
			Action:        item.Action,
			ArgumentsHash: item.ArgumentsHash,
		}
		if identity.Validate() != nil ||
			(item.IdentityVersion != "" &&
				item.IdentityVersion != idempotency.IdentityEffectV1) {
			return ErrInvalidEffectIdentity
		}
		if _, exists := effectIDs[item.EffectID]; exists {
			return ErrDuplicateEffect
		}
		effectIDs[item.EffectID] = struct{}{}
	}
	return nil
}

func validateProposal(value Approval) error {
	if value.ProposalRef != nil {
		if value.ProposalRef.ProposalID == "" ||
			value.ProposalRef.EventID == "" ||
			len(value.ProposalRef.Digest) != sha256.Size*2 {
			return fmt.Errorf("approval proposal reference is incomplete")
		}
		if _, err := hex.DecodeString(value.ProposalRef.Digest); err != nil {
			return fmt.Errorf("approval proposal digest is invalid")
		}
	}
	for _, evidence := range value.Evidence {
		if evidence.Source == nil {
			continue
		}
		if value.ProposalRef == nil ||
			evidence.Source.ToolCallID == "" ||
			evidence.Source.FieldPath == "" ||
			evidence.Source.SourceSeq == 0 {
			return fmt.Errorf("approval evidence source is incomplete")
		}
	}
	return nil
}

func legacyKeyCount(items []Item, key domain.IdempotencyKey) int {
	count := 0
	for _, item := range items {
		if item.IdentityVersion == "" && item.EffectID == "" && item.IdempotencyKey == key {
			count++
		}
	}
	return count
}
