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
)

var (
	ErrMissingKey    = errors.New("idempotency_key is required")
	ErrKeyConflict   = errors.New("idempotency key was already used for a different effect")
	ErrIndeterminate = errors.New("previous execution has no durable result")
)

type State string

const (
	StateStarted       State = "started"
	StateSucceeded     State = "succeeded"
	StateFailed        State = "failed"
	StateIndeterminate State = "indeterminate"
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

type entry struct {
	Command
	state   State
	result  json.RawMessage
	attempt int
	done    chan struct{}
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
	Key             domain.IdempotencyKey `json:"idempotency_key"`
	CallID          string                `json:"call_id"`
	Action          domain.Action         `json:"action"`
	ArgumentsHash   string                `json:"arguments_hash"`
	IdentityVersion IdentityVersion       `json:"identity_version,omitempty"`
	EffectID        domain.EffectID       `json:"effect_id,omitempty"`
	Attempt         int                   `json:"attempt,omitempty"`
	Result          json.RawMessage       `json:"result,omitempty"`
	Error           string                `json:"error,omitempty"`
}

type Store struct {
	journal *audit.Store

	mu      sync.Mutex
	entries map[domain.IdempotencyKey]*entry
}

func NewStore(journal *audit.Store) (*Store, error) {
	if journal == nil {
		return nil, fmt.Errorf("audit journal is required")
	}
	store := &Store{journal: journal, entries: make(map[domain.IdempotencyKey]*entry)}
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
				state:   StateIndeterminate,
				attempt: attempt,
				done:    closedChannel(),
			}
		case audit.EventWriteExecuted, audit.EventWriteFailed:
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
			if event.Type == audit.EventWriteFailed {
				state = StateFailed
			}
			store.entries[payload.Key] = &entry{
				Command: command,
				state:   state,
				result:  append(json.RawMessage(nil), payload.Result...),
				attempt: attempt,
				done:    closedChannel(),
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
	if command.Identity.Key == "" {
		return Result{}, ErrMissingKey
	}
	if command.RunID == "" || command.CallID == "" {
		return Result{}, fmt.Errorf("run id and call id are required")
	}
	if err := command.Identity.Validate(); err != nil {
		return Result{}, err
	}
	for {
		s.mu.Lock()
		attempt := 1
		existing := s.entries[command.Identity.Key]
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
			case StateStarted:
				done := existing.done
				s.mu.Unlock()
				select {
				case <-ctx.Done():
					return Result{}, ctx.Err()
				case <-done:
					continue
				}
			case StateIndeterminate:
				s.mu.Unlock()
				return Result{}, ErrIndeterminate
			case StateFailed:
				attempt = existing.attempt + 1
				delete(s.entries, command.Identity.Key)
			}
		}
		current := &entry{
			Command: command,
			state:   StateStarted,
			attempt: attempt,
			done:    make(chan struct{}),
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
			s.finish(command.Identity.Key, StateFailed, nil)
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
			Result:          value,
		}
		state := StateSucceeded
		if callErr != nil {
			eventType = audit.EventWriteFailed
			payload.Result = nil
			payload.Error = callErr.Error()
			state = StateFailed
		}
		_, journalErr := s.journal.Append(ctx, command.RunID, audit.Draft{
			EventID: writeEventID(command.Identity.EffectID, attempt, state),
			Actor:   audit.ActorSystem,
			Type:    eventType,
			Payload: payload,
		})
		if journalErr != nil {
			s.finish(command.Identity.Key, StateIndeterminate, nil)
			return Result{}, journalErr
		}
		s.finish(command.Identity.Key, state, value)
		if callErr != nil {
			return Result{}, callErr
		}
		return Result{Value: append(json.RawMessage(nil), value...)}, nil
	}
}

func (s *Store) Succeeded(command Command) bool {
	state, ok := s.Lookup(command)
	return ok && state == StateSucceeded
}

func (s *Store) Lookup(command Command) (State, bool) {
	if command.Identity.Validate() != nil {
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

func writeEventID(effectID domain.EffectID, attempt int, state State) string {
	return fmt.Sprintf("write:%s:attempt:%d:%s", effectID, attempt, state)
}

func closedChannel() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
