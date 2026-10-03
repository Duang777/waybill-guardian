package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

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

type LookupFunc func(context.Context, Command) (platform.EffectResult, error)

type entry struct {
	Command
	state           State
	result          json.RawMessage
	attempt         int
	reconciliations int
	done            chan struct{}
}

type writeStartedPayload struct {
	Key             domain.IdempotencyKey `json:"idempotency_key"`
	CallID          string                `json:"call_id"`
	Action          domain.Action         `json:"action"`
	ArgumentsHash   string                `json:"arguments_hash"`
	IdentityVersion IdentityVersion       `json:"identity_version,omitempty"`
	EffectID        domain.EffectID       `json:"effect_id,omitempty"`
	Attempt         int                   `json:"attempt,omitempty"`
}

type writeResultPayload struct {
	Key             domain.IdempotencyKey      `json:"idempotency_key"`
	CallID          string                     `json:"call_id"`
	Action          domain.Action              `json:"action"`
	ArgumentsHash   string                     `json:"arguments_hash"`
	IdentityVersion IdentityVersion            `json:"identity_version,omitempty"`
	EffectID        domain.EffectID            `json:"effect_id,omitempty"`
	Attempt         int                        `json:"attempt,omitempty"`
	Disposition     platform.EffectDisposition `json:"disposition,omitempty"`
	Result          json.RawMessage            `json:"result,omitempty"`
	Error           string                     `json:"error,omitempty"`
}

type reconciliationPayload struct {
	Key             domain.IdempotencyKey      `json:"idempotency_key"`
	CallID          string                     `json:"call_id"`
	Action          domain.Action              `json:"action"`
	ArgumentsHash   string                     `json:"arguments_hash"`
	IdentityVersion IdentityVersion            `json:"identity_version,omitempty"`
	EffectID        domain.EffectID            `json:"effect_id,omitempty"`
	Attempt         int                        `json:"attempt,omitempty"`
	Disposition     platform.EffectDisposition `json:"disposition,omitempty"`
	Result          json.RawMessage            `json:"result,omitempty"`
	Error           string                     `json:"error,omitempty"`
	Reconciliation  int                        `json:"reconciliation"`
}

type Store struct {
	journal audit.Journal
	lookup  LookupFunc

	mu      sync.Mutex
	entries map[domain.IdempotencyKey]*entry
}

func NewStore(journal audit.Journal, lookup LookupFunc) (*Store, error) {
	if journal == nil {
		return nil, fmt.Errorf("audit journal is required")
	}
	store := &Store{
		journal: journal,
		lookup:  lookup,
		entries: make(map[domain.IdempotencyKey]*entry),
	}
	for _, event := range journal.AllEvents() {
		switch event.Type {
		case audit.EventWriteStarted:
			var payload writeStartedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("rebuild idempotency start: %w", err)
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
				return nil, fmt.Errorf("rebuild idempotency start: %w", err)
			}
			store.entries[payload.Key] = &entry{
				Command: command,
				state:   StateUnknown,
				attempt: attempt,
				done:    closedChannel(),
			}
		case audit.EventWriteExecuted, audit.EventWriteFailed, audit.EventWriteUnknown:
			var payload writeResultPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("rebuild idempotency result: %w", err)
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
				return nil, fmt.Errorf("rebuild idempotency result: %w", err)
			}
			state := StateSucceeded
			switch event.Type {
			case audit.EventWriteFailed:
				state = stateForMutationDisposition(payload.Disposition)
			case audit.EventWriteUnknown:
				state = StateUnknown
			}
			store.entries[payload.Key] = &entry{
				Command: command,
				state:   state,
				result:  append(json.RawMessage(nil), payload.Result...),
				attempt: attempt,
				done:    closedChannel(),
			}
		case audit.EventWriteReconciliationStarted, audit.EventWriteReconciled:
			var payload reconciliationPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("rebuild idempotency reconciliation: %w", err)
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
				return nil, fmt.Errorf("rebuild idempotency reconciliation: %w", err)
			}
			state := StateUnknown
			result := json.RawMessage(nil)
			if event.Type == audit.EventWriteReconciled {
				state = stateForReconciliationDisposition(payload.Disposition)
				result = append(json.RawMessage(nil), payload.Result...)
			}
			store.entries[payload.Key] = &entry{
				Command:         command,
				state:           state,
				result:          result,
				attempt:         attempt,
				reconciliations: payload.Reconciliation,
				done:            closedChannel(),
			}
		}
	}
	return store, nil
}

// Generate preserves the schema-v1 key algorithm for journal compatibility.
func Generate(action domain.Action, waybillID domain.WaybillID, businessWindow string) domain.IdempotencyKey {
	sum := sha256.Sum256([]byte(string(action) + "|" + string(waybillID) + "|" + businessWindow))
	return domain.IdempotencyKey(hex.EncodeToString(sum[:]))
}

func ArgumentsHash(raw string) (string, error) {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return "", fmt.Errorf("decode tool arguments: %w", err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("canonicalize tool arguments: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Store) Execute(
	ctx context.Context,
	command Command,
	fn func(context.Context) (json.RawMessage, error),
) (Result, error) {
	if err := validateCommand(command); err != nil {
		return Result{}, err
	}
	for {
		s.mu.Lock()
		existing := s.entries[command.Identity.Key]
		attempt := 1
		if existing != nil {
			if !sameEffect(existing.Command, command) {
				s.mu.Unlock()
				return Result{}, ErrKeyConflict
			}
			switch existing.state {
			case StateSucceeded:
				value := append(json.RawMessage(nil), existing.result...)
				s.mu.Unlock()
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
				return Result{Value: value, Duplicate: true}, err
			case StateStarted, StateReconciling:
				done := existing.done
				s.mu.Unlock()
				select {
				case <-ctx.Done():
					return Result{}, ctx.Err()
				case <-done:
					continue
				}
			case StateUnknown:
				s.mu.Unlock()
				return s.Reconcile(ctx, command)
			case StateRetryableFailed:
				attempt = existing.attempt + 1
			case StatePermanentFailed:
				s.mu.Unlock()
				return Result{}, ErrPermanentFailure
			case StateManualReview:
				s.mu.Unlock()
				return Result{}, ErrManualReview
			}
		}
		current := &entry{
			Command:         command,
			state:           StateStarted,
			attempt:         attempt,
			reconciliations: reconciliationCount(existing),
			done:            make(chan struct{}),
		}
		s.entries[command.Identity.Key] = current
		s.mu.Unlock()

		_, err := s.journal.Append(ctx, command.RunID, audit.Draft{
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
			},
		})
		if err != nil {
			s.finish(command.Identity.Key, StateUnknown, nil)
			return Result{}, err
		}

		value, callErr := fn(ctx)
		eventType := audit.EventWriteExecuted
		payload := writeResultPayload{
			Key:             command.Identity.Key,
			CallID:          command.CallID,
			Action:          command.Identity.Action,
			ArgumentsHash:   command.Identity.ArgumentsHash,
			IdentityVersion: command.Identity.Version,
			EffectID:        command.Identity.EffectID,
			Attempt:         attempt,
			Disposition:     platform.EffectSucceeded,
			Result:          value,
		}
		state := StateSucceeded
		if callErr != nil {
			payload.Disposition = platform.EffectDispositionOf(callErr)
			payload.Result = nil
			payload.Error = callErr.Error()
			state = stateForMutationDisposition(payload.Disposition)
			if state == StateUnknown {
				eventType = audit.EventWriteUnknown
			} else {
				eventType = audit.EventWriteFailed
			}
		}
		_, journalErr := s.journal.Append(ctx, command.RunID, audit.Draft{
			EventID: writeEventID(command.Identity.EffectID, attempt, state),
			Actor:   audit.ActorSystem,
			Type:    eventType,
			Payload: payload,
		})
		if journalErr != nil {
			s.finish(command.Identity.Key, StateUnknown, nil)
			return Result{}, journalErr
		}
		s.finish(command.Identity.Key, state, value)
		if state == StateUnknown {
			result, reconcileErr := s.Reconcile(ctx, command)
			if reconcileErr != nil {
				return Result{}, errors.Join(callErr, reconcileErr)
			}
			return result, nil
		}
		if state == StateRetryableFailed {
			return Result{}, callErr
		}
		if state == StatePermanentFailed {
			return Result{}, errors.Join(callErr, ErrPermanentFailure)
		}
		return Result{Value: append(json.RawMessage(nil), value...)}, nil
	}
}

func (s *Store) Reconcile(ctx context.Context, command Command) (Result, error) {
	if err := validateCommand(command); err != nil {
		return Result{}, err
	}
	for {
		s.mu.Lock()
		current := s.entries[command.Identity.Key]
		if current == nil {
			s.mu.Unlock()
			return Result{}, ErrReconciliationPending
		}
		if !sameEffect(current.Command, command) {
			s.mu.Unlock()
			return Result{}, ErrKeyConflict
		}
		switch current.state {
		case StateSucceeded:
			value := append(json.RawMessage(nil), current.result...)
			s.mu.Unlock()
			return Result{Value: value, Duplicate: true}, nil
		case StateRetryableFailed:
			s.mu.Unlock()
			return Result{}, ErrRetryableFailure
		case StatePermanentFailed:
			s.mu.Unlock()
			return Result{}, ErrPermanentFailure
		case StateManualReview:
			s.mu.Unlock()
			return Result{}, ErrManualReview
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
		}
		if s.lookup == nil {
			s.mu.Unlock()
			return Result{}, errors.Join(ErrReconciliationPending, ErrReconciliationDisabled)
		}
		reconciliation := current.reconciliations + 1
		current.state = StateReconciling
		current.reconciliations = reconciliation
		current.done = make(chan struct{})
		s.mu.Unlock()

		started := reconciliationPayload{
			Key:             command.Identity.Key,
			CallID:          command.CallID,
			Action:          command.Identity.Action,
			ArgumentsHash:   command.Identity.ArgumentsHash,
			IdentityVersion: command.Identity.Version,
			EffectID:        command.Identity.EffectID,
			Attempt:         current.attempt,
			Reconciliation:  reconciliation,
		}
		if _, err := s.journal.Append(ctx, command.RunID, audit.Draft{
			EventID: reconciliationEventID(command.Identity.EffectID, reconciliation, StateStarted),
			Actor:   audit.ActorSystem,
			Type:    audit.EventWriteReconciliationStarted,
			Payload: started,
		}); err != nil {
			s.finish(command.Identity.Key, StateUnknown, nil)
			return Result{}, err
		}

		effect, lookupErr := s.lookup(ctx, command)
		if effect.Disposition == "" {
			effect.Disposition = platform.EffectDispositionOf(lookupErr)
		}
		if effect.Disposition == platform.EffectSucceeded && len(effect.Response) == 0 {
			lookupErr = errors.Join(lookupErr, errors.New("effect lookup returned success without a response"))
			effect.Disposition = platform.EffectPermanentFailed
		}
		state := stateForReconciliationDisposition(effect.Disposition)
		payload := started
		payload.Disposition = effect.Disposition
		payload.Result = effect.Response
		if lookupErr != nil {
			payload.Error = lookupErr.Error()
		}
		if _, err := s.journal.Append(ctx, command.RunID, audit.Draft{
			EventID: reconciliationEventID(command.Identity.EffectID, reconciliation, state),
			Actor:   audit.ActorSystem,
			Type:    audit.EventWriteReconciled,
			Payload: payload,
		}); err != nil {
			s.finish(command.Identity.Key, StateUnknown, nil)
			return Result{}, err
		}
		s.finish(command.Identity.Key, state, effect.Response)
		switch state {
		case StateSucceeded:
			return Result{Value: append(json.RawMessage(nil), effect.Response...), Duplicate: true}, nil
		case StateRetryableFailed:
			return Result{}, errors.Join(lookupErr, ErrRetryableFailure)
		case StateManualReview:
			return Result{}, errors.Join(lookupErr, ErrManualReview)
		default:
			return Result{}, errors.Join(lookupErr, ErrReconciliationPending)
		}
	}
}

func (s *Store) Succeeded(command Command) bool {
	state, ok := s.Lookup(command)
	return ok && state == StateSucceeded
}

func (s *Store) Lookup(command Command) (State, bool) {
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

func validateCommand(command Command) error {
	if command.Identity.Key == "" {
		return ErrMissingKey
	}
	if command.RunID == "" || command.CallID == "" {
		return fmt.Errorf("run id and call id are required")
	}
	return command.Identity.Validate()
}

func (s *Store) finish(key domain.IdempotencyKey, state State, result json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.entries[key]
	if current == nil {
		return
	}
	current.state = state
	current.result = append(json.RawMessage(nil), result...)
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
	case platform.EffectUnknown, "":
		return StateUnknown
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
	case platform.EffectUnknown, "":
		return StateUnknown
	default:
		return StateUnknown
	}
}

func reconciliationCount(current *entry) int {
	if current == nil {
		return 0
	}
	return current.reconciliations
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
