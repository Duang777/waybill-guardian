package idempotency

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform"
)

func TestAuthorizeEffectCanonicalizesAndCopiesArguments(t *testing.T) {
	command, request := authorizedEffectFixture(t)
	original := append(json.RawMessage(nil), request.Arguments...)

	effect, err := AuthorizeEffect(command, request)
	if err != nil {
		t.Fatal(err)
	}
	request.Arguments[0] = '['

	if effect.Command() != command {
		t.Fatalf("command = %+v, want %+v", effect.Command(), command)
	}
	got := effect.Request()
	want := json.RawMessage(`{"carrier_id":"CARRIER-SW-42","waybill_id":"YD2026101001"}`)
	if !bytes.Equal(got.Arguments, want) {
		t.Fatalf("arguments = %s, want %s; original = %s", got.Arguments, want, original)
	}
	if got.Action != command.Identity.Action || got.ArgumentsHash != command.Identity.ArgumentsHash {
		t.Fatalf("request = %+v", got)
	}

	got.Arguments[0] = '['
	if next := effect.Request(); !bytes.Equal(next.Arguments, want) {
		t.Fatalf("authorized arguments changed through accessor: %s", next.Arguments)
	}
}

func TestAuthorizeEffectRejectsMismatchedRequest(t *testing.T) {
	command, request := authorizedEffectFixture(t)
	otherArguments := json.RawMessage(
		`{"waybill_id":"YD2026101001","carrier_id":"CARRIER-OTHER"}`,
	)

	tests := []struct {
		name    string
		command Command
		request platform.EffectRequest
		want    error
	}{
		{
			name:    "invalid command",
			command: Command{},
			request: request,
			want:    ErrMissingKey,
		},
		{
			name:    "read action",
			command: command,
			request: platform.EffectRequest{
				Action:        domain.ActionGetWaybill,
				Arguments:     request.Arguments,
				ArgumentsHash: request.ArgumentsHash,
			},
			want: ErrInvalidEffectRequest,
		},
		{
			name:    "different write action",
			command: command,
			request: platform.EffectRequest{
				Action:        domain.ActionCreateClaim,
				Arguments:     request.Arguments,
				ArgumentsHash: request.ArgumentsHash,
			},
			want: ErrInvalidEffectRequest,
		},
		{
			name:    "missing hash",
			command: command,
			request: platform.EffectRequest{
				Action:    request.Action,
				Arguments: request.Arguments,
			},
			want: ErrInvalidEffectRequest,
		},
		{
			name:    "identity hash mismatch",
			command: command,
			request: platform.EffectRequest{
				Action:        request.Action,
				Arguments:     request.Arguments,
				ArgumentsHash: "different",
			},
			want: ErrInvalidEffectRequest,
		},
		{
			name:    "arguments changed after hashing",
			command: command,
			request: platform.EffectRequest{
				Action:        request.Action,
				Arguments:     otherArguments,
				ArgumentsHash: request.ArgumentsHash,
			},
			want: ErrInvalidEffectRequest,
		},
		{
			name:    "invalid json",
			command: command,
			request: platform.EffectRequest{
				Action:        request.Action,
				Arguments:     json.RawMessage(`{"waybill_id":`),
				ArgumentsHash: request.ArgumentsHash,
			},
			want: ErrInvalidEffectRequest,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := AuthorizeEffect(test.command, test.request); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func authorizedEffectFixture(t *testing.T) (Command, platform.EffectRequest) {
	t.Helper()
	arguments := json.RawMessage(
		`{"waybill_id":"YD2026101001","carrier_id":"CARRIER-SW-42"}`,
	)
	identity, err := Derive(DerivationInput{
		RunContext: domain.RunContext{
			RunID:       "run-authorized",
			IncidentID:  "incident-authorized",
			WaybillID:   "YD2026101001",
			PlanVersion: 1,
		},
		Action:    domain.ActionReassign,
		Target:    "waybill/YD2026101001",
		Arguments: arguments,
	})
	if err != nil {
		t.Fatal(err)
	}
	return Command{
			RunID:    "run-authorized",
			CallID:   "call-authorized",
			Identity: identity,
		}, platform.EffectRequest{
			Action: domain.ActionReassign,
			Arguments: json.RawMessage(
				`{"carrier_id":"CARRIER-SW-42","waybill_id":"YD2026101001"}`,
			),
			ArgumentsHash: identity.ArgumentsHash,
		}
}
