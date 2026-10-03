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
	"github.com/google/uuid"
)

var (
	ErrMissingKey    = errors.New("idempotency_key is required")
	ErrKeyConflict   = errors.New("idempotency key was already used with different arguments")
	ErrIndeterminate = errors.New("previous execution has no durable result")
)

var effectNamespace = uuid.MustParse("cb7d4ee2-ec1d-5a91-bf19-bc062ac8bcaf")

type State string

const (
	StateStarted       State = "started"
	StateSucceeded     State = "succeeded"
	StateFailed        State = "failed"
	StateIndeterminate State = "indeterminate"
)

type Command struct {
	RunID         domain.RunID
	CallID        string
	Action        domain.Action
	EffectID      domain.EffectID
	Key           domain.IdempotencyKey
	ArgumentsHash string
}

type Identity struct {
	EffectID domain.EffectID
	Key      domain.IdempotencyKey
}

type Result struct {
	Value     json.RawMessage
	Duplicate bool
}

type entry struct {
	Command
	state  State
	result json.RawMessage
	done   chan struct{}
}

type writeStartedPayload struct {
	Key           domain.IdempotencyKey `json:"idempotency_key"`
	EffectID      domain.EffectID       `json:"effect_id,omitempty"`
	CallID        string                `json:"call_id"`
	Action        domain.Action         `json:"action"`
	ArgumentsHash string                `json:"arguments_hash"`
}

type writeResultPayload struct {
	Key           domain.IdempotencyKey `json:"idempotency_key"`
	EffectID      domain.EffectID       `json:"effect_id,omitempty"`
	CallID        string                `json:"call_id"`
	Action        domain.Action         `json:"action"`
	ArgumentsHash string                `json:"arguments_hash"`
	Result        json.RawMessage       `json:"result,omitempty"`
	Error         string                `json:"error,omitempty"`
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
			store.entries[payload.Key] = &entry{
				Command: Command{
					RunID:         event.RunID,
					CallID:        payload.CallID,
					Action:        payload.Action,
					EffectID:      payload.EffectID,
					Key:           payload.Key,
					ArgumentsHash: payload.ArgumentsHash,
				},
				state: StateIndeterminate,
				done:  closedChannel(),
			}
		case audit.EventWriteExecuted, audit.EventWriteFailed:
			var payload writeResultPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("rebuild idempotency result: %w", err)
			}
			state := StateSucceeded
			if event.Type == audit.EventWriteFailed {
				state = StateFailed
			}
			store.entries[payload.Key] = &entry{
				Command: Command{
					RunID:         event.RunID,
					CallID:        payload.CallID,
					Action:        payload.Action,
					EffectID:      payload.EffectID,
					Key:           payload.Key,
					ArgumentsHash: payload.ArgumentsHash,
				},
				state:  state,
				result: append(json.RawMessage(nil), payload.Result...),
				done:   closedChannel(),
			}
		}
	}
	return store, nil
}

func Identify(
	action domain.Action,
	waybillID domain.WaybillID,
	businessWindow string,
	argumentsHash string,
) Identity {
	name := string(action) + "|" + string(waybillID) + "|" + businessWindow + "|" + argumentsHash
	effectID := domain.EffectID(uuid.NewSHA1(effectNamespace, []byte(name)).String())
	sum := sha256.Sum256([]byte(effectID))
	return Identity{
		EffectID: effectID,
		Key:      domain.IdempotencyKey(hex.EncodeToString(sum[:])),
	}
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
	if command.Key == "" {
		return Result{}, ErrMissingKey
	}
	if command.RunID == "" || command.CallID == "" || command.Action == "" ||
		command.EffectID == "" || command.ArgumentsHash == "" {
		return Result{}, fmt.Errorf("run id, call id, action, effect id, and arguments hash are required")
	}
	for {
		s.mu.Lock()
		existing := s.entries[command.Key]
		if existing != nil {
			if existing.ArgumentsHash != command.ArgumentsHash ||
				existing.Action != command.Action ||
				existing.EffectID != command.EffectID {
				s.mu.Unlock()
				return Result{}, ErrKeyConflict
			}
			switch existing.state {
			case StateSucceeded:
				value := append(json.RawMessage(nil), existing.result...)
				s.mu.Unlock()
				_, err := s.journal.Append(ctx, command.RunID, audit.Draft{
					EventID: "duplicate:" + string(command.Key) + ":" + command.CallID,
					Actor:   audit.ActorSystem,
					Type:    audit.EventDuplicateSuppressed,
					Payload: map[string]any{
						"idempotency_key": command.Key,
						"effect_id":       command.EffectID,
						"action":          command.Action,
						"call_id":         command.CallID,
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
				delete(s.entries, command.Key)
			}
		}
		current := &entry{Command: command, state: StateStarted, done: make(chan struct{})}
		s.entries[command.Key] = current
		s.mu.Unlock()

		_, err := s.journal.Append(ctx, command.RunID, audit.Draft{
			EventID: "write:" + string(command.Key) + ":started",
			Actor:   audit.ActorSystem,
			Type:    audit.EventWriteStarted,
			Payload: writeStartedPayload{
				Key:           command.Key,
				EffectID:      command.EffectID,
				CallID:        command.CallID,
				Action:        command.Action,
				ArgumentsHash: command.ArgumentsHash,
			},
		})
		if err != nil {
			s.finish(command.Key, StateFailed, nil)
			return Result{}, err
		}

		value, callErr := fn(ctx)
		eventType := audit.EventWriteExecuted
		payload := writeResultPayload{
			Key:           command.Key,
			EffectID:      command.EffectID,
			CallID:        command.CallID,
			Action:        command.Action,
			ArgumentsHash: command.ArgumentsHash,
			Result:        value,
		}
		state := StateSucceeded
		if callErr != nil {
			eventType = audit.EventWriteFailed
			payload.Result = nil
			payload.Error = callErr.Error()
			state = StateFailed
		}
		_, journalErr := s.journal.Append(ctx, command.RunID, audit.Draft{
			EventID: "write:" + string(command.Key) + ":" + string(state),
			Actor:   audit.ActorSystem,
			Type:    eventType,
			Payload: payload,
		})
		if journalErr != nil {
			s.finish(command.Key, StateIndeterminate, nil)
			return Result{}, journalErr
		}
		s.finish(command.Key, state, value)
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
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.entries[command.Key]
	if current == nil ||
		current.RunID != command.RunID ||
		current.Action != command.Action ||
		current.EffectID != command.EffectID ||
		current.ArgumentsHash != command.ArgumentsHash {
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

func closedChannel() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
