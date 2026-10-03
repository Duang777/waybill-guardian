package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
)

func TestGenerate(t *testing.T) {
	first := Generate(domain.ActionReassign, "YD2026101001", "incident-1/plan-1")
	second := Generate(domain.ActionReassign, "YD2026101001", "incident-1/plan-1")
	other := Generate(domain.ActionReassign, "YD2026101001", "incident-2/plan-1")
	if first != second {
		t.Fatal("same business operation produced different keys")
	}
	if first == other {
		t.Fatal("different business windows produced the same key")
	}
}

func TestConcurrentExecuteRunsEffectOnce(t *testing.T) {
	journal, err := audit.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(journal)
	if err != nil {
		t.Fatal(err)
	}
	key := Generate(domain.ActionReassign, "YD2026101001", "incident-1/plan-1")
	var calls atomic.Int64
	const workers = 10
	results := make([]Result, workers)
	errs := make([]error, workers)
	identity := mustLegacyIdentity(t, domain.ActionReassign, key, "args-hash")
	var wait sync.WaitGroup
	wait.Add(workers)
	for index := 0; index < workers; index++ {
		go func(index int) {
			defer wait.Done()
			results[index], errs[index] = store.Execute(context.Background(), Command{
				RunID:    "run-concurrent",
				CallID:   "call-" + string(rune('a'+index)),
				Identity: identity,
			}, func(context.Context) (json.RawMessage, error) {
				calls.Add(1)
				time.Sleep(10 * time.Millisecond)
				return json.RawMessage(`{"order_id":"RA-1"}`), nil
			})
		}(index)
	}
	wait.Wait()

	if calls.Load() != 1 {
		t.Fatalf("effect calls = %d, want 1", calls.Load())
	}
	duplicates := 0
	for index, err := range errs {
		if err != nil {
			t.Fatalf("worker %d failed: %v", index, err)
		}
		if string(results[index].Value) != `{"order_id":"RA-1"}` {
			t.Fatalf("worker %d result = %s", index, results[index].Value)
		}
		if results[index].Duplicate {
			duplicates++
		}
	}
	if duplicates != workers-1 {
		t.Fatalf("duplicates = %d, want %d", duplicates, workers-1)
	}
}

func TestMissingAndConflictingKeysAreRejected(t *testing.T) {
	journal, err := audit.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(journal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Execute(context.Background(), Command{}, func(context.Context) (json.RawMessage, error) {
		return nil, nil
	}); !errors.Is(err, ErrMissingKey) {
		t.Fatalf("missing key error = %v", err)
	}

	key := domain.IdempotencyKey("same-key")
	_, err = store.Execute(context.Background(), Command{
		RunID:    "run-1",
		CallID:   "call-1",
		Identity: mustLegacyIdentity(t, domain.ActionReassign, key, "first"),
	}, func(context.Context) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Execute(context.Background(), Command{
		RunID:    "run-1",
		CallID:   "call-2",
		Identity: mustLegacyIdentity(t, domain.ActionReassign, key, "second"),
	}, func(context.Context) (json.RawMessage, error) {
		t.Fatal("conflicting effect must not run")
		return nil, nil
	})
	if !errors.Is(err, ErrKeyConflict) {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestSucceededMatchesDurableCommand(t *testing.T) {
	journal, err := audit.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(journal)
	if err != nil {
		t.Fatal(err)
	}
	command := Command{
		RunID:    "run-1",
		CallID:   "call-1",
		Identity: mustLegacyIdentity(t, domain.ActionReassign, "key-1", "args-hash"),
	}
	if store.Succeeded(command) {
		t.Fatal("missing command reported as succeeded")
	}
	if _, err := store.Execute(context.Background(), command, func(context.Context) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	}); err != nil {
		t.Fatal(err)
	}
	if !store.Succeeded(command) {
		t.Fatal("successful command was not found")
	}
	command.Identity.ArgumentsHash = "different"
	if store.Succeeded(command) {
		t.Fatal("mismatched command reported as succeeded")
	}
}

func TestFailedExecutionRetriesWithDistinctAttemptEvents(t *testing.T) {
	journal, err := audit.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(journal)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := Derive(DerivationInput{
		RunContext: domain.RunContext{
			RunID:       "run-retry",
			IncidentID:  "incident-retry",
			WaybillID:   "YD2026101001",
			PlanVersion: 1,
		},
		Action:    domain.ActionSendSMS,
		Target:    "phone/13800001234",
		Arguments: json.RawMessage(`{"phone":"13800001234","template_id":"delay","params":{}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	command := Command{RunID: "run-retry", CallID: "call-retry", Identity: identity}
	for attempt := 1; attempt <= 2; attempt++ {
		_, err := store.Execute(context.Background(), command, func(context.Context) (json.RawMessage, error) {
			return nil, errors.New("temporary failure")
		})
		if err == nil {
			t.Fatalf("attempt %d succeeded", attempt)
		}
	}

	events, err := journal.Replay(context.Background(), command.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var eventIDs []string
	for _, event := range events {
		eventIDs = append(eventIDs, event.EventID)
	}
	expected := []string{
		"write:" + string(identity.EffectID) + ":attempt:1:started",
		"write:" + string(identity.EffectID) + ":attempt:1:failed",
		"write:" + string(identity.EffectID) + ":attempt:2:started",
		"write:" + string(identity.EffectID) + ":attempt:2:failed",
	}
	if len(eventIDs) != len(expected) {
		t.Fatalf("event ids = %v", eventIDs)
	}
	for index := range expected {
		if eventIDs[index] != expected[index] {
			t.Fatalf("event %d = %q, want %q", index, eventIDs[index], expected[index])
		}
	}
}

func TestNewStoreReplaysLegacySuccessWithoutExecuting(t *testing.T) {
	journal, err := audit.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	payload := writeResultPayload{
		Key:           "legacy-key",
		CallID:        "legacy-call",
		Action:        domain.ActionReassign,
		ArgumentsHash: "legacy-arguments-hash",
		Result:        json.RawMessage(`{"order_id":"RA-legacy"}`),
	}
	if _, err := journal.Append(context.Background(), "legacy-run", audit.Draft{
		EventID: "write:legacy-key:succeeded",
		Actor:   audit.ActorSystem,
		Type:    audit.EventWriteExecuted,
		Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(journal)
	if err != nil {
		t.Fatal(err)
	}
	command := Command{
		RunID:    "legacy-run",
		CallID:   "legacy-call",
		Identity: mustLegacyIdentity(t, payload.Action, payload.Key, payload.ArgumentsHash),
	}
	result, err := store.Execute(context.Background(), command, func(context.Context) (json.RawMessage, error) {
		t.Fatal("legacy success executed again")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Duplicate || string(result.Value) != `{"order_id":"RA-legacy"}` {
		t.Fatalf("legacy replay = %+v", result)
	}
}

func TestNewStoreReplaysEffectV0SuccessWithoutExecuting(t *testing.T) {
	journal, err := audit.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	runContext := domain.RunContext{
		RunID:       "effect-v0-run",
		IncidentID:  "effect-v0-incident",
		WaybillID:   "YD2026101001",
		PlanVersion: 1,
	}
	identity, err := EffectV0Identity(runContext, domain.ActionSendSMS, "effect-v0-hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(context.Background(), runContext.RunID, audit.Draft{
		EventID: "write:" + string(identity.Key) + ":succeeded",
		Actor:   audit.ActorSystem,
		Type:    audit.EventWriteExecuted,
		Payload: writeResultPayload{
			Key:           identity.Key,
			EffectID:      identity.EffectID,
			CallID:        "effect-v0-call",
			Action:        identity.Action,
			ArgumentsHash: identity.ArgumentsHash,
			Result:        json.RawMessage(`{"message_id":"SMS-v0"}`),
		},
	}); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(journal)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Execute(context.Background(), Command{
		RunID:    runContext.RunID,
		CallID:   "effect-v0-call",
		Identity: identity,
	}, func(context.Context) (json.RawMessage, error) {
		t.Fatal("effect-v0 success executed again")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Duplicate || string(result.Value) != `{"message_id":"SMS-v0"}` {
		t.Fatalf("effect-v0 replay = %+v", result)
	}
}

func TestNewStoreRejectsPartiallyPopulatedIdentity(t *testing.T) {
	journal, err := audit.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(context.Background(), "run-partial-identity", audit.Draft{
		EventID: "write:partial:started",
		Actor:   audit.ActorSystem,
		Type:    audit.EventWriteStarted,
		Payload: writeStartedPayload{
			Key:             "key",
			CallID:          "call",
			Action:          domain.ActionReassign,
			ArgumentsHash:   "hash",
			IdentityVersion: IdentityEffectV1,
			Attempt:         1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(journal); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("partial identity error = %v", err)
	}
}

func mustLegacyIdentity(
	t *testing.T,
	action domain.Action,
	key domain.IdempotencyKey,
	argumentsHash string,
) Identity {
	t.Helper()
	identity, err := LegacyIdentity(action, key, argumentsHash)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}
