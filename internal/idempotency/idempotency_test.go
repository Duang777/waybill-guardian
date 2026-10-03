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
	var wait sync.WaitGroup
	wait.Add(workers)
	for index := 0; index < workers; index++ {
		go func(index int) {
			defer wait.Done()
			results[index], errs[index] = store.Execute(context.Background(), Command{
				RunID:         "run-concurrent",
				CallID:        "call-" + string(rune('a'+index)),
				Action:        domain.ActionReassign,
				Key:           key,
				ArgumentsHash: "args-hash",
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
		RunID: "run-1", CallID: "call-1", Action: domain.ActionReassign, Key: key, ArgumentsHash: "first",
	}, func(context.Context) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Execute(context.Background(), Command{
		RunID: "run-1", CallID: "call-2", Action: domain.ActionReassign, Key: key, ArgumentsHash: "second",
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
		RunID:         "run-1",
		CallID:        "call-1",
		Action:        domain.ActionReassign,
		Key:           "key-1",
		ArgumentsHash: "args-hash",
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
	command.ArgumentsHash = "different"
	if store.Succeeded(command) {
		t.Fatal("mismatched command reported as succeeded")
	}
}
