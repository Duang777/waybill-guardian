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
	"github.com/Duang777/waybill-guardian/internal/platform"
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
	defer journal.Close()
	runtime := &testWriteRuntime{
		dispatch: func(context.Context, platform.EffectBinding, platform.EffectRequest, domain.IdempotencyKey) platform.DispatchResult {
			time.Sleep(10 * time.Millisecond)
			return platform.DispatchResult{
				Disposition: platform.EffectSucceeded,
				Response:    json.RawMessage(`{"order_id":"RA-1"}`),
			}
		},
	}
	store, err := NewStore(journal, StoreConfig{Runtime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	const workers = 10
	results := make([]Result, workers)
	errs := make([]error, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for index := 0; index < workers; index++ {
		go func(index int) {
			defer wait.Done()
			command := testCommand(
				t,
				"run-concurrent",
				"call-"+string(rune('a'+index)),
				"shared-key",
				domain.ActionReassign,
				`{"waybill_id":"YD2026101001","carrier_id":"CARRIER-SW-42"}`,
			)
			results[index], errs[index] = store.Execute(
				context.Background(),
				testAuthorizedEffect(t, command, `{"waybill_id":"YD2026101001","carrier_id":"CARRIER-SW-42"}`),
			)
		}(index)
	}
	wait.Wait()

	if runtime.dispatchCalls.Load() != 1 {
		t.Fatalf("effect calls = %d, want 1", runtime.dispatchCalls.Load())
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

func TestConflictingKeyIsRejected(t *testing.T) {
	journal, err := audit.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	runtime := successfulTestRuntime()
	store, err := NewStore(journal, StoreConfig{Runtime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	firstArgs := `{"waybill_id":"YD2026101001","carrier_id":"CARRIER-SW-42"}`
	first := testCommand(t, "run-1", "call-1", "same-key", domain.ActionReassign, firstArgs)
	if _, err := store.Execute(context.Background(), testAuthorizedEffect(t, first, firstArgs)); err != nil {
		t.Fatal(err)
	}
	secondArgs := `{"waybill_id":"YD2026101001","carrier_id":"CARRIER-SW-19"}`
	second := testCommand(t, "run-1", "call-2", "same-key", domain.ActionReassign, secondArgs)
	if _, err := store.Execute(
		context.Background(),
		testAuthorizedEffect(t, second, secondArgs),
	); !errors.Is(err, ErrKeyConflict) {
		t.Fatalf("conflict error = %v, want ErrKeyConflict", err)
	}
	if runtime.dispatchCalls.Load() != 1 {
		t.Fatalf("dispatch calls = %d, want 1", runtime.dispatchCalls.Load())
	}
}

func TestStatusMatchesDurableCommand(t *testing.T) {
	journal, err := audit.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	store, err := NewStore(journal, StoreConfig{Runtime: successfulTestRuntime()})
	if err != nil {
		t.Fatal(err)
	}
	args := `{"waybill_id":"YD2026101001","claim_type":"delay"}`
	command := testCommand(t, "run-status", "call-status", "key-status", domain.ActionCreateClaim, args)
	if _, ok := store.Status(command); ok {
		t.Fatal("missing command reported a status")
	}
	if _, err := store.Execute(context.Background(), testAuthorizedEffect(t, command, args)); err != nil {
		t.Fatal(err)
	}
	state, ok := store.Status(command)
	if !ok || state != StateSucceeded {
		t.Fatalf("status = %q, %v, want succeeded", state, ok)
	}
	command.Identity.ArgumentsHash = "different"
	if _, ok := store.Status(command); ok {
		t.Fatal("mismatched command reported a status")
	}
}

func TestRetryableExecutionUsesDistinctAttemptEvents(t *testing.T) {
	journal, err := audit.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	runtime := &testWriteRuntime{
		dispatch: func(context.Context, platform.EffectBinding, platform.EffectRequest, domain.IdempotencyKey) platform.DispatchResult {
			return platform.DispatchResult{
				Disposition: platform.EffectRetryableFailed,
				ErrorCode:   "temporary_failure",
			}
		},
	}
	store, err := NewStore(journal, StoreConfig{Runtime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	args := `{"waybill_id":"YD2026101001","claim_type":"delay"}`
	command := testCommand(t, "run-retry", "call-retry", "key-retry", domain.ActionCreateClaim, args)
	effect := testAuthorizedEffect(t, command, args)
	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := store.Execute(context.Background(), effect); !errors.Is(err, ErrRetryableFailure) {
			t.Fatalf("attempt %d error = %v, want ErrRetryableFailure", attempt, err)
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
		"write:" + string(command.Identity.EffectID) + ":attempt:1:started",
		"write:" + string(command.Identity.EffectID) + ":attempt:1:retryable_failed",
		"write:" + string(command.Identity.EffectID) + ":attempt:2:started",
		"write:" + string(command.Identity.EffectID) + ":attempt:2:retryable_failed",
	}
	if len(eventIDs) != len(expected) {
		t.Fatalf("event IDs = %v, want %v", eventIDs, expected)
	}
	for index := range expected {
		if eventIDs[index] != expected[index] {
			t.Fatalf("event %d = %q, want %q", index, eventIDs[index], expected[index])
		}
	}
}

func TestRecoverKeepsDelayedRetryPending(t *testing.T) {
	journal, err := audit.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	runtime := &testWriteRuntime{
		dispatch: func(context.Context, platform.EffectBinding, platform.EffectRequest, domain.IdempotencyKey) platform.DispatchResult {
			return platform.DispatchResult{
				Disposition: platform.EffectRetryableFailed,
				ErrorCode:   "rate_limited",
				RetryAfter:  time.Minute,
			}
		},
	}
	store, err := NewStore(journal, StoreConfig{
		Runtime: runtime,
		Clock:   func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	args := `{"waybill_id":"YD2026101001","claim_type":"delay"}`
	command := testCommand(t, "run-delay", "call-delay", "key-delay", domain.ActionCreateClaim, args)
	if _, err := store.Execute(
		context.Background(),
		testAuthorizedEffect(t, command, args),
	); !errors.Is(err, ErrRetryableFailure) {
		t.Fatalf("Execute error = %v, want ErrRetryableFailure", err)
	}
	outcome, err := store.Recover(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Decision != RecoveryPending ||
		outcome.State != StateRetryableFailed ||
		!outcome.RetryAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("recovery outcome = %+v, want delayed pending retry", outcome)
	}
}

func TestNewStoreReplaysLegacySuccessWithoutDispatching(t *testing.T) {
	journal, err := audit.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	args := `{"waybill_id":"YD2026101001","carrier_id":"CARRIER-SW-42"}`
	command := testCommand(t, "legacy-run", "legacy-call", "legacy-key", domain.ActionReassign, args)
	if _, err := journal.Append(context.Background(), command.RunID, audit.Draft{
		EventID: "write:legacy-key:succeeded",
		Actor:   audit.ActorSystem,
		Type:    audit.EventWriteExecuted,
		Payload: writeResultPayload{
			Key:           command.Identity.Key,
			CallID:        command.CallID,
			Action:        command.Identity.Action,
			ArgumentsHash: command.Identity.ArgumentsHash,
			Result:        json.RawMessage(`{"order_id":"RA-legacy"}`),
		},
	}); err != nil {
		t.Fatal(err)
	}
	runtime := successfulTestRuntime()
	store, err := NewStore(journal, StoreConfig{Runtime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Execute(context.Background(), testAuthorizedEffect(t, command, args))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Duplicate || string(result.Value) != `{"order_id":"RA-legacy"}` {
		t.Fatalf("legacy replay = %+v", result)
	}
	if runtime.dispatchCalls.Load() != 0 {
		t.Fatalf("legacy success dispatched %d times", runtime.dispatchCalls.Load())
	}
}

func TestNewStoreRejectsPartiallyPopulatedIdentity(t *testing.T) {
	journal, err := audit.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
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
	if _, err := NewStore(journal, StoreConfig{}); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("partial identity error = %v", err)
	}
}

func TestStartedWithoutBindingRequiresManualReview(t *testing.T) {
	journal, err := audit.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	args := `{"waybill_id":"YD2026101001","carrier_id":"CARRIER-SW-42"}`
	command := testCommand(t, "run-crashed", "call-crashed", "key-crashed", domain.ActionReassign, args)
	if _, err := journal.Append(context.Background(), command.RunID, audit.Draft{
		EventID: "write:" + string(command.Identity.EffectID) + ":attempt:1:started",
		Actor:   audit.ActorSystem,
		Type:    audit.EventWriteStarted,
		Payload: writeStartedPayload{
			Key:             command.Identity.Key,
			EffectID:        command.Identity.EffectID,
			CallID:          command.CallID,
			Action:          command.Identity.Action,
			ArgumentsHash:   command.Identity.ArgumentsHash,
			IdentityVersion: command.Identity.Version,
			Attempt:         1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(journal, StoreConfig{Runtime: successfulTestRuntime()})
	if err != nil {
		t.Fatal(err)
	}
	state, ok := store.Status(command)
	if !ok || state != StateUnknown {
		t.Fatalf("crashed command state = %q, %v, want unknown", state, ok)
	}
	outcome, err := store.Recover(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Decision != RecoveryManualReview || outcome.State != StateManualReview {
		t.Fatalf("recovery outcome = %+v, want manual review", outcome)
	}
}

func TestUnknownMutationUsesLookupInsteadOfRepeatingDispatch(t *testing.T) {
	journal, err := audit.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	runtime := &testWriteRuntime{
		dispatch: func(context.Context, platform.EffectBinding, platform.EffectRequest, domain.IdempotencyKey) platform.DispatchResult {
			return platform.DispatchResult{
				Disposition: platform.EffectUnknown,
				ErrorCode:   "response_lost",
			}
		},
		lookup: func(context.Context, platform.EffectBinding, domain.IdempotencyKey) platform.LookupResult {
			return platform.LookupResult{
				Disposition: platform.LookupApplied,
				Response:    json.RawMessage(`{"order_id":"RA-committed"}`),
			}
		},
	}
	store, err := NewStore(journal, StoreConfig{Runtime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	args := `{"waybill_id":"YD2026101001","carrier_id":"CARRIER-SW-42"}`
	command := testCommand(t, "run-unknown", "call-unknown", "key-unknown", domain.ActionReassign, args)
	result, err := store.Execute(context.Background(), testAuthorizedEffect(t, command, args))
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.Execute(context.Background(), testAuthorizedEffect(t, command, args))
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Value) != `{"order_id":"RA-committed"}` || !replayed.Duplicate {
		t.Fatalf("results = first:%+v replayed:%+v", result, replayed)
	}
	if runtime.dispatchCalls.Load() != 1 || runtime.lookupCalls.Load() != 1 {
		t.Fatalf(
			"calls = dispatch:%d lookup:%d, want 1 each",
			runtime.dispatchCalls.Load(),
			runtime.lookupCalls.Load(),
		)
	}
}

type testWriteRuntime struct {
	dispatchCalls atomic.Int64
	lookupCalls   atomic.Int64
	dispatch      func(
		context.Context,
		platform.EffectBinding,
		platform.EffectRequest,
		domain.IdempotencyKey,
	) platform.DispatchResult
	lookup func(
		context.Context,
		platform.EffectBinding,
		domain.IdempotencyKey,
	) platform.LookupResult
}

func successfulTestRuntime() *testWriteRuntime {
	return &testWriteRuntime{
		dispatch: func(context.Context, platform.EffectBinding, platform.EffectRequest, domain.IdempotencyKey) platform.DispatchResult {
			return platform.DispatchResult{
				Disposition: platform.EffectSucceeded,
				Response:    json.RawMessage(`{"ok":true}`),
			}
		},
	}
}

func (r *testWriteRuntime) AdvertisedActions() []domain.Action {
	return []domain.Action{
		domain.ActionReassign,
		domain.ActionCreateClaim,
		domain.ActionSendSMS,
	}
}

func (r *testWriteRuntime) Bind(
	request platform.EffectRequest,
	_ domain.IdempotencyKey,
	createdAt time.Time,
) (platform.EffectBinding, error) {
	return platform.EffectBinding{
		SchemaVersion:           1,
		Action:                  request.Action,
		AdapterID:               "test-runtime",
		ContractVersion:         "v1",
		ProviderOperation:       string(request.Action),
		ProviderScopeDigest:     "scope",
		ProviderRequestHash:     request.ArgumentsHash,
		KeyCreatedAt:            createdAt,
		KeyExpiresAt:            createdAt.Add(time.Hour),
		LookupConsistencyWindow: 0,
	}, nil
}

func (r *testWriteRuntime) Dispatch(
	ctx context.Context,
	binding platform.EffectBinding,
	request platform.EffectRequest,
	key domain.IdempotencyKey,
) platform.DispatchResult {
	r.dispatchCalls.Add(1)
	if r.dispatch == nil {
		return platform.DispatchResult{Disposition: platform.EffectUnknown}
	}
	return r.dispatch(ctx, binding, request, key)
}

func (r *testWriteRuntime) Lookup(
	ctx context.Context,
	binding platform.EffectBinding,
	key domain.IdempotencyKey,
) platform.LookupResult {
	r.lookupCalls.Add(1)
	if r.lookup == nil {
		return platform.LookupResult{Disposition: platform.LookupPending}
	}
	return r.lookup(ctx, binding, key)
}

func (r *testWriteRuntime) SupportsRecovery(binding platform.EffectBinding) bool {
	return binding.SchemaVersion == 1 &&
		binding.AdapterID == "test-runtime" &&
		binding.KeyExpiresAt.After(binding.KeyCreatedAt)
}

func testCommand(
	t *testing.T,
	runID domain.RunID,
	callID string,
	key domain.IdempotencyKey,
	action domain.Action,
	arguments string,
) Command {
	t.Helper()
	hash, err := ArgumentsHash(arguments)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := LegacyIdentity(action, key, hash)
	if err != nil {
		t.Fatal(err)
	}
	return Command{RunID: runID, CallID: callID, Identity: identity}
}

func testAuthorizedEffect(
	t *testing.T,
	command Command,
	arguments string,
) AuthorizedEffect {
	t.Helper()
	effect, err := AuthorizeEffect(command, platform.EffectRequest{
		Action:        command.Identity.Action,
		Arguments:     json.RawMessage(arguments),
		ArgumentsHash: command.Identity.ArgumentsHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	return effect
}

var _ platform.WriteRuntime = (*testWriteRuntime)(nil)
