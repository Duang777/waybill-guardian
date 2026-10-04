package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform"
)

func TestFixtureWriteRuntimeDispatchesAndLooksUpByStableKey(t *testing.T) {
	_, runtime, err := NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	request := platform.EffectRequest{
		Action:        domain.ActionReassign,
		Arguments:     json.RawMessage(`{"carrier_id":"CARRIER-SW-42","waybill_id":"YD2026101001"}`),
		ArgumentsHash: "arguments-hash",
	}
	key := domain.IdempotencyKey("fixture-effect-key")
	createdAt := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	binding, err := runtime.Bind(request, key, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if !runtime.SupportsRecovery(binding) {
		t.Fatalf("runtime rejected binding %+v", binding)
	}

	first := runtime.Dispatch(context.Background(), binding, request, key)
	second := runtime.Dispatch(context.Background(), binding, request, key)
	if first.Disposition != platform.EffectSucceeded ||
		second.Disposition != platform.EffectSucceeded ||
		string(first.Response) != string(second.Response) {
		t.Fatalf("dispatch results = first:%+v second:%+v", first, second)
	}
	if runtime.WriteCount(domain.ActionReassign) != 1 {
		t.Fatalf("reassign writes = %d, want 1", runtime.WriteCount(domain.ActionReassign))
	}
	lookedUp := runtime.Lookup(context.Background(), binding, key)
	if lookedUp.Disposition != platform.LookupApplied ||
		string(lookedUp.Response) != string(first.Response) {
		t.Fatalf("lookup result = %+v", lookedUp)
	}
}

func TestFixtureWriteRuntimeTreatsBusinessRejectionAsPermanent(t *testing.T) {
	_, runtime, err := NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	request := platform.EffectRequest{
		Action: domain.ActionReassign,
		Arguments: json.RawMessage(
			`{"carrier_id":"CARRIER-NOT-CANDIDATE","waybill_id":"YD2026101001"}`,
		),
		ArgumentsHash: "arguments-hash",
	}
	binding, err := runtime.Bind(
		request,
		"rejected-fixture-effect",
		time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	result := runtime.Dispatch(
		t.Context(),
		binding,
		request,
		"rejected-fixture-effect",
	)
	if result.Disposition != platform.EffectPermanentFailed {
		t.Fatalf("dispatch disposition = %q, want permanent_failed", result.Disposition)
	}
	if runtime.WriteCount(domain.ActionReassign) != 0 {
		t.Fatal("rejected reassign was recorded as a write")
	}
}

func TestFixtureWriteRuntimeRejectsIncompleteRecoveryBindings(t *testing.T) {
	_, runtime, err := NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	request := platform.EffectRequest{
		Action:        domain.ActionReassign,
		Arguments:     json.RawMessage(`{"carrier_id":"CARRIER-SW-42","waybill_id":"YD2026101001"}`),
		ArgumentsHash: "arguments-hash",
	}
	binding, err := runtime.Bind(
		request,
		"fixture-effect-key",
		time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		change func(*platform.EffectBinding)
	}{
		{
			name: "missing request hash",
			change: func(value *platform.EffectBinding) {
				value.ProviderRequestHash = ""
			},
		},
		{
			name: "invalid request hash",
			change: func(value *platform.EffectBinding) {
				value.ProviderRequestHash = "not-a-digest"
			},
		},
		{
			name: "missing creation time",
			change: func(value *platform.EffectBinding) {
				value.KeyCreatedAt = time.Time{}
				value.KeyExpiresAt = value.KeyCreatedAt.Add(fixtureRuntimeKeyRetention)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := binding
			test.change(&changed)
			if runtime.SupportsRecovery(changed) {
				t.Fatalf("runtime accepted incomplete binding %+v", changed)
			}
		})
	}
}

func TestFixtureWriteRuntimeReportsAuthoritativeAbsence(t *testing.T) {
	_, runtime, err := NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	request := platform.EffectRequest{
		Action:        domain.ActionCreateClaim,
		Arguments:     json.RawMessage(`{"claim_type":"delay","waybill_id":"YD2026101001"}`),
		ArgumentsHash: "arguments-hash",
	}
	binding, err := runtime.Bind(
		request,
		"missing-fixture-effect",
		time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	result := runtime.Lookup(context.Background(), binding, "missing-fixture-effect")
	if result.Disposition != platform.LookupAbsent {
		t.Fatalf("lookup disposition = %q, want authoritative_absent", result.Disposition)
	}
}

func TestWriteHandlersRequireMiddleware(t *testing.T) {
	clients, runtime, err := NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := NewHandlers(clients)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Reassign(context.Background(), ReassignInput{
		WaybillID: "YD2026101001",
		CarrierID: "CARRIER-SW-42",
	}); !errors.Is(err, ErrWriteMiddlewareRequired) {
		t.Fatalf("Reassign error = %v, want ErrWriteMiddlewareRequired", err)
	}
	if runtime.WriteCount(domain.ActionReassign) != 0 {
		t.Fatalf("direct handler dispatched %d writes", runtime.WriteCount(domain.ActionReassign))
	}
}
