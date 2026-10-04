package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform"
)

var (
	ErrMissingKey             = errors.New("idempotency_key is required")
	ErrKeyConflict            = errors.New("idempotency key was already used for a different effect")
	ErrRetryableFailure       = errors.New("effect failed before the platform committed")
	ErrPermanentFailure       = errors.New("effect permanently failed")
	ErrReconciliationPending  = errors.New("effect result requires reconciliation")
	ErrManualReview           = errors.New("effect reconciliation requires manual review")
	ErrReconciliationDisabled = errors.New("effect lookup is not configured")
)

const defaultRecoveryRetry = time.Second

type State string

const (
	StateStarted         State = "started"
	StateSucceeded       State = "succeeded"
	StateRetryableFailed State = "retryable_failed"
	StatePermanentFailed State = "permanent_failed"
	StateUnknown         State = "unknown"
	StateReconciling     State = "reconciling"
	StateManualReview    State = "manual_review"
)

type Command struct {
	RunID    domain.RunID
	CallID   string
	Identity Identity
}

type Result struct {
	Value     json.RawMessage
	Duplicate bool
}

type Executor interface {
	Execute(context.Context, AuthorizedEffect) (Result, error)
	Recover(context.Context, Command) (RecoveryOutcome, error)
	Status(Command) (State, bool)
}

type RecoverySource interface {
	DueRecoveries(context.Context) ([]Command, error)
}

type StoreConfig struct {
	Runtime platform.WriteRuntime
	Clock   func() time.Time
}

type entry struct {
	Command
	state           State
	result          json.RawMessage
	binding         platform.EffectBinding
	retryAt         time.Time
	attempt         int
	reconciliations int
	done            chan struct{}
}

type writeStartedPayload struct {
	Key             domain.IdempotencyKey   `json:"idempotency_key"`
	CallID          string                  `json:"call_id"`
	Action          domain.Action           `json:"action"`
	ArgumentsHash   string                  `json:"arguments_hash"`
	IdentityVersion IdentityVersion         `json:"identity_version,omitempty"`
	EffectID        domain.EffectID         `json:"effect_id,omitempty"`
	Attempt         int                     `json:"attempt,omitempty"`
	Binding         *platform.EffectBinding `json:"binding,omitempty"`
	StartedAt       time.Time               `json:"dispatch_started_at,omitempty"`
}

type writeResultPayload struct {
	Key               domain.IdempotencyKey      `json:"idempotency_key"`
	CallID            string                     `json:"call_id"`
	Action            domain.Action              `json:"action"`
	ArgumentsHash     string                     `json:"arguments_hash"`
	IdentityVersion   IdentityVersion            `json:"identity_version,omitempty"`
	EffectID          domain.EffectID            `json:"effect_id,omitempty"`
	Attempt           int                        `json:"attempt,omitempty"`
	State             State                      `json:"state,omitempty"`
	Disposition       platform.EffectDisposition `json:"disposition,omitempty"`
	LookupDisposition platform.LookupDisposition `json:"lookup_disposition,omitempty"`
	Result            json.RawMessage            `json:"result,omitempty"`
	Error             string                     `json:"error,omitempty"`
	ErrorCode         string                     `json:"error_code,omitempty"`
	ErrorClass        string                     `json:"error_class,omitempty"`
	ExternalRef       string                     `json:"external_ref,omitempty"`
	ExternalRequestID string                     `json:"external_request_id,omitempty"`
	ResponseDigest    string                     `json:"response_digest,omitempty"`
	RetryAt           *time.Time                 `json:"retry_at,omitempty"`
	LastLookupAt      *time.Time                 `json:"last_lookup_at,omitempty"`
	Reconciliation    int                        `json:"reconciliation,omitempty"`
}

type reconciliationPayload = writeResultPayload

type Store struct {
	journal audit.Journal
	runtime platform.WriteRuntime
	clock   func() time.Time

	mu      sync.Mutex
	entries map[domain.IdempotencyKey]*entry
}

func NewStore(journal audit.Journal, config StoreConfig) (*Store, error) {
	if journal == nil {
		return nil, fmt.Errorf("audit journal is required")
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	store := &Store{
		journal: journal,
		runtime: config.Runtime,
		clock:   config.Clock,
		entries: make(map[domain.IdempotencyKey]*entry),
	}
	events, err := journal.AllEvents(context.Background())
	if err != nil {
		return nil, fmt.Errorf("read idempotency events: %w", err)
	}
	for _, event := range events {
		switch event.Type {
		case audit.EventWriteStarted:
			if err := store.rebuildStart(event); err != nil {
				return nil, err
			}
		case audit.EventWriteExecuted, audit.EventWriteFailed, audit.EventWriteUnknown:
			if err := store.rebuildResult(event); err != nil {
				return nil, err
			}
		case audit.EventWriteReconciliationStarted, audit.EventWriteReconciled:
			if err := store.rebuildReconciliation(event); err != nil {
				return nil, err
			}
		}
	}
	return store, nil
}

func (s *Store) rebuildStart(event audit.Event) error {
	var payload writeStartedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("rebuild idempotency start: %w", err)
	}
	command, attempt, err := commandFromPayload(
		event.RunID,
		payload.CallID,
		payload.Action,
		payload.Key,
		payload.ArgumentsHash,
		payload.IdentityVersion,
		payload.EffectID,
		payload.Attempt,
	)
	if err != nil {
		return fmt.Errorf("rebuild idempotency start: %w", err)
	}
	current := &entry{
		Command: command,
		state:   StateUnknown,
		attempt: attempt,
		done:    closedChannel(),
	}
	if payload.Binding != nil {
		current.binding = *payload.Binding
	}
	s.entries[payload.Key] = current
	return nil
}

func (s *Store) rebuildResult(event audit.Event) error {
	var payload writeResultPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("rebuild idempotency result: %w", err)
	}
	command, attempt, err := commandFromPayload(
		event.RunID,
		payload.CallID,
		payload.Action,
		payload.Key,
		payload.ArgumentsHash,
		payload.IdentityVersion,
		payload.EffectID,
		payload.Attempt,
	)
	if err != nil {
		return fmt.Errorf("rebuild idempotency result: %w", err)
	}
	state := payload.State
	if state == "" {
		state = StateSucceeded
		switch event.Type {
		case audit.EventWriteFailed:
			state = stateForMutationDisposition(payload.Disposition)
		case audit.EventWriteUnknown:
			state = StateUnknown
		}
	}
	current := s.entries[payload.Key]
	s.entries[payload.Key] = &entry{
		Command:         command,
		state:           state,
		result:          append(json.RawMessage(nil), payload.Result...),
		binding:         bindingOf(current),
		retryAt:         timeValue(payload.RetryAt),
		attempt:         attempt,
		reconciliations: reconciliationCount(current),
		done:            closedChannel(),
	}
	return nil
}

func (s *Store) rebuildReconciliation(event audit.Event) error {
	var payload reconciliationPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("rebuild idempotency reconciliation: %w", err)
	}
	command, attempt, err := commandFromPayload(
		event.RunID,
		payload.CallID,
		payload.Action,
		payload.Key,
		payload.ArgumentsHash,
		payload.IdentityVersion,
		payload.EffectID,
		payload.Attempt,
	)
	if err != nil {
		return fmt.Errorf("rebuild idempotency reconciliation: %w", err)
	}
	state := StateUnknown
	result := json.RawMessage(nil)
	if event.Type == audit.EventWriteReconciled {
		state = payload.State
		if state == "" {
			state = stateForReconciliationDisposition(payload.Disposition)
		}
		result = append(json.RawMessage(nil), payload.Result...)
	}
	current := s.entries[payload.Key]
	s.entries[payload.Key] = &entry{
		Command:         command,
		state:           state,
		result:          result,
		binding:         bindingOf(current),
		retryAt:         timeValue(payload.RetryAt),
		attempt:         attempt,
		reconciliations: payload.Reconciliation,
		done:            closedChannel(),
	}
	return nil
}

// Generate preserves the schema-v1 key algorithm for journal compatibility.
func Generate(action domain.Action, waybillID domain.WaybillID, businessWindow string) domain.IdempotencyKey {
	sum := sha256.Sum256([]byte(string(action) + "|" + string(waybillID) + "|" + businessWindow))
	return domain.IdempotencyKey(hex.EncodeToString(sum[:]))
}

func ArgumentsHash(raw string) (string, error) {
	canonical, err := canonicalArguments(json.RawMessage(raw))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func canonicalArguments(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("decode tool arguments: %w", err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("canonicalize tool arguments: %w", err)
	}
	return canonical, nil
}

func (s *Store) Execute(ctx context.Context, effect AuthorizedEffect) (Result, error) {
	command := effect.Command()
	request := effect.Request()
	if err := validateCommand(command); err != nil {
		return Result{}, err
	}
	for {
		s.mu.Lock()
		current := s.entries[command.Identity.Key]
		attempt := 1
		reconciliations := 0
		var binding platform.EffectBinding
		if current != nil {
			if !sameEffect(current.Command, command) {
				s.mu.Unlock()
				return Result{}, ErrKeyConflict
			}
			attempt = current.attempt
			reconciliations = current.reconciliations
			binding = current.binding
			switch current.state {
			case StateSucceeded:
				value := append(json.RawMessage(nil), current.result...)
				s.mu.Unlock()
				if err := s.recordDuplicate(ctx, command); err != nil {
					return Result{}, err
				}
				return Result{Value: value, Duplicate: true}, nil
			case StateStarted, StateReconciling:
				done := current.done
				s.mu.Unlock()
				select {
				case <-ctx.Done():
					return Result{}, ctx.Err()
				case <-done:
					continue
				}
			case StateUnknown:
				s.mu.Unlock()
				outcome, err := s.Recover(ctx, command)
				if err != nil {
					return Result{}, err
				}
				switch outcome.Decision {
				case RecoveryResolved:
					return outcome.Result, nil
				case RecoveryReadyToResume:
					continue
				case RecoveryBusy:
					continue
				case RecoveryPermanentFailure:
					return Result{}, ErrPermanentFailure
				case RecoveryManualReview:
					return Result{}, ErrManualReview
				default:
					return Result{}, ErrReconciliationPending
				}
			case StateRetryableFailed:
				if !current.retryAt.IsZero() && s.clock().UTC().Before(current.retryAt) {
					s.mu.Unlock()
					return Result{}, ErrRetryableFailure
				}
				attempt++
			case StatePermanentFailed:
				s.mu.Unlock()
				return Result{}, ErrPermanentFailure
			case StateManualReview:
				s.mu.Unlock()
				return Result{}, ErrManualReview
			}
		}
		if s.runtime == nil {
			s.mu.Unlock()
			return Result{}, ErrReconciliationDisabled
		}
		if binding.SchemaVersion == 0 {
			var err error
			binding, err = s.runtime.Bind(request, command.Identity.Key, s.clock().UTC())
			if err != nil {
				s.mu.Unlock()
				return Result{}, fmt.Errorf("bind effect runtime: %w", err)
			}
		}
		if binding.Action != command.Identity.Action ||
			!s.runtime.SupportsRecovery(binding) {
			s.mu.Unlock()
			return Result{}, ErrManualReview
		}
		current = &entry{
			Command:         command,
			state:           StateStarted,
			binding:         binding,
			attempt:         attempt,
			reconciliations: reconciliations,
			done:            make(chan struct{}),
		}
		s.entries[command.Identity.Key] = current
		s.mu.Unlock()

		startedAt := s.clock().UTC()
		if _, err := s.journal.Append(ctx, command.RunID, audit.Draft{
			EventID: writeEventID(command.Identity.EffectID, attempt, StateStarted),
			Actor:   audit.ActorSystem,
			Type:    audit.EventWriteStarted,
			Payload: writeStartedPayload{
				Key:             command.Identity.Key,
				CallID:          command.CallID,
				Action:          command.Identity.Action,
				ArgumentsHash:   command.Identity.ArgumentsHash,
				IdentityVersion: command.Identity.Version,
				EffectID:        command.Identity.EffectID,
				Attempt:         attempt,
				Binding:         &binding,
				StartedAt:       startedAt,
			},
		}); err != nil {
			s.finish(command.Identity.Key, StateUnknown, nil, time.Time{})
			return Result{}, err
		}

		dispatched := normalizeDispatchResult(s.runtime.Dispatch(
			ctx,
			binding,
			request,
			command.Identity.Key,
		))
		state := stateForMutationDisposition(dispatched.Disposition)
		retryAt := retryTime(s.clock().UTC(), dispatched.RetryAfter, false)
		eventType := audit.EventWriteExecuted
		switch state {
		case StateRetryableFailed, StatePermanentFailed:
			eventType = audit.EventWriteFailed
		case StateUnknown:
			eventType = audit.EventWriteUnknown
		}
		payload := writeResultPayload{
			Key:               command.Identity.Key,
			CallID:            command.CallID,
			Action:            command.Identity.Action,
			ArgumentsHash:     command.Identity.ArgumentsHash,
			IdentityVersion:   command.Identity.Version,
			EffectID:          command.Identity.EffectID,
			Attempt:           attempt,
			State:             state,
			Disposition:       dispatched.Disposition,
			Result:            append(json.RawMessage(nil), dispatched.Response...),
			ErrorCode:         dispatched.ErrorCode,
			ErrorClass:        dispatchErrorClass(dispatched.Disposition),
			ExternalRef:       dispatched.ExternalRef,
			ExternalRequestID: dispatched.ExternalRequestID,
			ResponseDigest:    dispatched.ResponseDigest,
			RetryAt:           timePointer(retryAt),
		}
		if _, err := s.journal.Append(ctx, command.RunID, audit.Draft{
			EventID: writeEventID(command.Identity.EffectID, attempt, state),
			Actor:   audit.ActorSystem,
			Type:    eventType,
			Payload: payload,
		}); err != nil {
			s.finish(command.Identity.Key, StateUnknown, nil, retryAt)
			return Result{}, err
		}
		s.finish(command.Identity.Key, state, dispatched.Response, retryAt)
		switch state {
		case StateSucceeded:
			return Result{Value: append(json.RawMessage(nil), dispatched.Response...)}, nil
		case StateRetryableFailed:
			return Result{}, effectResultError(ErrRetryableFailure, dispatched.ErrorCode)
		case StatePermanentFailed:
			return Result{}, effectResultError(ErrPermanentFailure, dispatched.ErrorCode)
		default:
			outcome, err := s.Recover(ctx, command)
			if err != nil {
				return Result{}, err
			}
			if outcome.Decision == RecoveryResolved {
				return outcome.Result, nil
			}
			return Result{}, recoveryError(outcome.Decision)
		}
	}
}

func (s *Store) Recover(ctx context.Context, command Command) (RecoveryOutcome, error) {
	if err := validateCommand(command); err != nil {
		return RecoveryOutcome{}, err
	}
	s.mu.Lock()
	current := s.entries[command.Identity.Key]
	if current == nil {
		s.mu.Unlock()
		return RecoveryOutcome{
			Decision: RecoveryReadyToResume,
			State:    StateRetryableFailed,
		}, nil
	}
	if !sameEffect(current.Command, command) {
		s.mu.Unlock()
		return RecoveryOutcome{}, ErrKeyConflict
	}
	switch current.state {
	case StateSucceeded:
		result := Result{
			Value:     append(json.RawMessage(nil), current.result...),
			Duplicate: true,
		}
		s.mu.Unlock()
		return RecoveryOutcome{
			Decision: RecoveryResolved,
			Result:   result,
			State:    StateSucceeded,
		}, nil
	case StateRetryableFailed:
		if !current.retryAt.IsZero() && s.clock().UTC().Before(current.retryAt) {
			outcome := RecoveryOutcome{
				Decision: RecoveryPending,
				State:    StateRetryableFailed,
				RetryAt:  current.retryAt,
			}
			s.mu.Unlock()
			return outcome, nil
		}
		if current.binding.SchemaVersion != 0 &&
			!s.clock().UTC().Before(current.binding.KeyExpiresAt) {
			break
		}
		s.mu.Unlock()
		return RecoveryOutcome{
			Decision: RecoveryReadyToResume,
			State:    StateRetryableFailed,
			RetryAt:  current.retryAt,
		}, nil
	case StatePermanentFailed:
		s.mu.Unlock()
		return RecoveryOutcome{
			Decision: RecoveryPermanentFailure,
			State:    StatePermanentFailed,
		}, nil
	case StateManualReview:
		s.mu.Unlock()
		return RecoveryOutcome{
			Decision: RecoveryManualReview,
			State:    StateManualReview,
		}, nil
	case StateStarted, StateReconciling:
		s.mu.Unlock()
		return RecoveryOutcome{
			Decision: RecoveryBusy,
			State:    current.state,
		}, nil
	case StateUnknown:
		if !current.retryAt.IsZero() && s.clock().UTC().Before(current.retryAt) {
			outcome := RecoveryOutcome{
				Decision: RecoveryPending,
				State:    StateUnknown,
				RetryAt:  current.retryAt,
			}
			s.mu.Unlock()
			return outcome, nil
		}
	}

	reconciliation := current.reconciliations + 1
	current.state = StateReconciling
	current.reconciliations = reconciliation
	current.done = make(chan struct{})
	attempt := current.attempt
	binding := current.binding
	s.mu.Unlock()

	started := reconciliationPayload{
		Key:             command.Identity.Key,
		CallID:          command.CallID,
		Action:          command.Identity.Action,
		ArgumentsHash:   command.Identity.ArgumentsHash,
		IdentityVersion: command.Identity.Version,
		EffectID:        command.Identity.EffectID,
		Attempt:         attempt,
		State:           StateReconciling,
		Reconciliation:  reconciliation,
	}
	if _, err := s.journal.Append(ctx, command.RunID, audit.Draft{
		EventID: reconciliationEventID(command.Identity.EffectID, reconciliation, StateStarted),
		Actor:   audit.ActorSystem,
		Type:    audit.EventWriteReconciliationStarted,
		Payload: started,
	}); err != nil {
		s.finish(command.Identity.Key, StateUnknown, nil, time.Time{})
		return RecoveryOutcome{}, err
	}

	now := s.clock().UTC()
	var lookedUp platform.LookupResult
	lastLookupAt := now
	switch {
	case s.runtime == nil:
		lookedUp = platform.LookupResult{
			Disposition: platform.LookupConflict,
			ErrorCode:   "recovery_runtime_unavailable",
		}
		lastLookupAt = time.Time{}
	case binding.SchemaVersion == 0 || !s.runtime.SupportsRecovery(binding):
		lookedUp = platform.LookupResult{
			Disposition: platform.LookupConflict,
			ErrorCode:   "recovery_binding_unavailable",
		}
		lastLookupAt = time.Time{}
	case !now.Before(binding.KeyExpiresAt):
		lookedUp = platform.LookupResult{
			Disposition: platform.LookupConflict,
			ErrorCode:   "key_expired",
		}
		lastLookupAt = time.Time{}
	default:
		lookedUp = normalizeLookupResult(s.runtime.Lookup(ctx, binding, command.Identity.Key))
	}

	state, decision := stateForLookup(lookedUp.Disposition)
	retryAt := retryTime(now, lookedUp.RetryAfter, state == StateUnknown)
	payload := started
	payload.State = state
	payload.LookupDisposition = lookedUp.Disposition
	payload.Result = append(json.RawMessage(nil), lookedUp.Response...)
	payload.ErrorCode = lookedUp.ErrorCode
	payload.ErrorClass = lookupErrorClass(lookedUp.Disposition)
	payload.ExternalRef = lookedUp.ExternalRef
	payload.ExternalRequestID = lookedUp.ExternalRequestID
	payload.ResponseDigest = lookedUp.ResponseDigest
	payload.RetryAt = timePointer(retryAt)
	payload.LastLookupAt = timePointer(lastLookupAt)
	if _, err := s.journal.Append(ctx, command.RunID, audit.Draft{
		EventID: reconciliationEventID(command.Identity.EffectID, reconciliation, state),
		Actor:   audit.ActorSystem,
		Type:    audit.EventWriteReconciled,
		Payload: payload,
	}); err != nil {
		s.finish(command.Identity.Key, StateUnknown, nil, retryAt)
		return RecoveryOutcome{}, err
	}
	s.finish(command.Identity.Key, state, lookedUp.Response, retryAt)

	outcome := RecoveryOutcome{
		Decision: decision,
		State:    state,
		RetryAt:  retryAt,
	}
	if decision == RecoveryResolved {
		outcome.Result = Result{
			Value:     append(json.RawMessage(nil), lookedUp.Response...),
			Duplicate: true,
		}
	}
	return outcome, nil
}

func (s *Store) Status(command Command) (State, bool) {
	if validateCommand(command) != nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.entries[command.Identity.Key]
	if current == nil || !sameEffect(current.Command, command) {
		return "", false
	}
	return current.state, true
}

func (s *Store) recordDuplicate(ctx context.Context, command Command) error {
	_, err := s.journal.Append(ctx, command.RunID, audit.Draft{
		EventID: "duplicate:" + string(command.Identity.EffectID) + ":" + command.CallID,
		Actor:   audit.ActorSystem,
		Type:    audit.EventDuplicateSuppressed,
		Payload: map[string]any{
			"identity_version": command.Identity.Version,
			"effect_id":        command.Identity.EffectID,
			"idempotency_key":  command.Identity.Key,
			"action":           command.Identity.Action,
			"call_id":          command.CallID,
		},
	})
	return err
}

func validateCommand(command Command) error {
	if command.Identity.Key == "" {
		return ErrMissingKey
	}
	if command.RunID == "" || command.CallID == "" {
		return fmt.Errorf("run id and call id are required")
	}
	return command.Identity.Validate()
}

func (s *Store) finish(
	key domain.IdempotencyKey,
	state State,
	result json.RawMessage,
	retryAt time.Time,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.entries[key]
	if current == nil {
		return
	}
	current.state = state
	current.result = append(json.RawMessage(nil), result...)
	current.retryAt = retryAt
	close(current.done)
	current.done = closedChannel()
}

func sameEffect(left, right Command) bool {
	return left.RunID == right.RunID &&
		left.Identity.Version == right.Identity.Version &&
		left.Identity.EffectID == right.Identity.EffectID &&
		left.Identity.Key == right.Identity.Key &&
		left.Identity.Action == right.Identity.Action &&
		left.Identity.ArgumentsHash == right.Identity.ArgumentsHash
}

func commandFromPayload(
	runID domain.RunID,
	callID string,
	action domain.Action,
	key domain.IdempotencyKey,
	argumentsHash string,
	version IdentityVersion,
	effectID domain.EffectID,
	attempt int,
) (Command, int, error) {
	if runID == "" || callID == "" {
		return Command{}, 0, ErrInvalidIdentity
	}
	var identity Identity
	var err error
	switch {
	case version == "" && effectID == "":
		identity, err = LegacyIdentity(action, key, argumentsHash)
		if attempt == 0 {
			attempt = 1
		}
	case version == "" && effectID != "" && attempt == 0:
		identity = Identity{
			Version:       IdentityEffectV0,
			EffectID:      effectID,
			Key:           key,
			Action:        action,
			ArgumentsHash: argumentsHash,
		}
		err = identity.Validate()
		attempt = 1
	case version == "" || effectID == "" || attempt <= 0:
		return Command{}, 0, ErrInvalidIdentity
	default:
		identity = Identity{
			Version:       version,
			EffectID:      effectID,
			Key:           key,
			Action:        action,
			ArgumentsHash: argumentsHash,
		}
		err = identity.Validate()
	}
	if err != nil {
		return Command{}, 0, err
	}
	return Command{RunID: runID, CallID: callID, Identity: identity}, attempt, nil
}

func stateForMutationDisposition(disposition platform.EffectDisposition) State {
	switch disposition {
	case platform.EffectSucceeded:
		return StateSucceeded
	case platform.EffectRetryableFailed:
		return StateRetryableFailed
	case platform.EffectPermanentFailed:
		return StatePermanentFailed
	default:
		return StateUnknown
	}
}

func stateForReconciliationDisposition(disposition platform.EffectDisposition) State {
	switch disposition {
	case platform.EffectSucceeded:
		return StateSucceeded
	case platform.EffectRetryableFailed:
		return StateRetryableFailed
	case platform.EffectPermanentFailed:
		return StateManualReview
	default:
		return StateUnknown
	}
}

func stateForLookup(disposition platform.LookupDisposition) (State, RecoveryDecision) {
	switch disposition {
	case platform.LookupApplied:
		return StateSucceeded, RecoveryResolved
	case platform.LookupRejected:
		return StatePermanentFailed, RecoveryPermanentFailure
	case platform.LookupAbsent:
		return StateRetryableFailed, RecoveryReadyToResume
	case platform.LookupConflict:
		return StateManualReview, RecoveryManualReview
	default:
		return StateUnknown, RecoveryPending
	}
}

func normalizeDispatchResult(result platform.DispatchResult) platform.DispatchResult {
	switch result.Disposition {
	case platform.EffectSucceeded:
		canonical, err := canonicalEffectResponse(result.Response)
		if err != nil {
			result.Disposition = platform.EffectUnknown
			result.Response = nil
			result.ErrorCode = "invalid_response"
		} else {
			result.Response = canonical
		}
	case platform.EffectRetryableFailed, platform.EffectPermanentFailed, platform.EffectUnknown:
	default:
		result.Disposition = platform.EffectUnknown
		result.ErrorCode = "invalid_disposition"
	}
	return result
}

func normalizeLookupResult(result platform.LookupResult) platform.LookupResult {
	switch result.Disposition {
	case platform.LookupApplied:
		canonical, err := canonicalEffectResponse(result.Response)
		if err != nil {
			result.Disposition = platform.LookupConflict
			result.Response = nil
			result.ErrorCode = "invalid_response"
		} else {
			result.Response = canonical
		}
	case platform.LookupRejected, platform.LookupAbsent, platform.LookupPending,
		platform.LookupConflict:
	default:
		result.Disposition = platform.LookupPending
		result.ErrorCode = "invalid_disposition"
	}
	return result
}

func canonicalEffectResponse(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("effect response is empty")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func retryTime(now time.Time, delay time.Duration, useDefault bool) time.Time {
	if delay > 0 {
		return now.Add(delay)
	}
	if useDefault {
		return now.Add(defaultRecoveryRetry)
	}
	return time.Time{}
}

func dispatchErrorClass(disposition platform.EffectDisposition) string {
	switch disposition {
	case platform.EffectRetryableFailed:
		return "dispatch_retryable"
	case platform.EffectPermanentFailed:
		return "dispatch_permanent"
	case platform.EffectUnknown:
		return "dispatch_unknown"
	default:
		return ""
	}
}

func lookupErrorClass(disposition platform.LookupDisposition) string {
	switch disposition {
	case platform.LookupRejected:
		return "lookup_rejected"
	case platform.LookupAbsent:
		return "lookup_absent"
	case platform.LookupPending:
		return "lookup_pending"
	case platform.LookupConflict:
		return "lookup_conflict"
	default:
		return ""
	}
}

func recoveryError(decision RecoveryDecision) error {
	switch decision {
	case RecoveryReadyToResume:
		return ErrRetryableFailure
	case RecoveryPermanentFailure:
		return ErrPermanentFailure
	case RecoveryManualReview:
		return ErrManualReview
	default:
		return ErrReconciliationPending
	}
}

func effectResultError(sentinel error, code string) error {
	if code == "" {
		return sentinel
	}
	return errors.Join(sentinel, errors.New(code))
}

func reconciliationCount(current *entry) int {
	if current == nil {
		return 0
	}
	return current.reconciliations
}

func bindingOf(current *entry) platform.EffectBinding {
	if current == nil {
		return platform.EffectBinding{}
	}
	return current.binding
}

func timeValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}

func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	value = value.UTC()
	return &value
}

func writeEventID(effectID domain.EffectID, attempt int, state State) string {
	return fmt.Sprintf("write:%s:attempt:%d:%s", effectID, attempt, state)
}

func reconciliationEventID(effectID domain.EffectID, reconciliation int, state State) string {
	return fmt.Sprintf("write:%s:reconciliation:%d:%s", effectID, reconciliation, state)
}

func closedChannel() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

var _ Executor = (*Store)(nil)
