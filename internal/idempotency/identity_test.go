package idempotency

import (
	"testing"

	"github.com/Duang777/waybill-guardian/internal/domain"
)

func TestDeriveIdentityGoldenVector(t *testing.T) {
	input := DerivationInput{
		RunContext: domain.RunContext{
			RunID:       "run-identity",
			IncidentID:  "incident-identity",
			WaybillID:   "YD2026101001",
			PlanVersion: 2,
		},
		Action: domain.ActionSendSMS,
		Target: "phone/13800001234",
		Arguments: []byte(
			`{"phone":"13800001234","template_id":"waybill_reassigned","params":{"waybill_id":"YD2026101001","carrier_id":"CARRIER-SW-42"}}`,
		),
	}

	identity, err := Derive(input)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Version != IdentityEffectV1 {
		t.Fatalf("identity version = %q", identity.Version)
	}
	if identity.ArgumentsHash != "b5acc191a07fa8efc5acd595f15113123cbcfc3a9bf833a958dee1e7f44bf8e1" {
		t.Fatalf("arguments hash = %q", identity.ArgumentsHash)
	}
	if identity.EffectID != "1fd92e48-c2d3-5654-b9f9-a507a2348354" {
		t.Fatalf("effect id = %q", identity.EffectID)
	}
	if identity.Key != "4dfe361bea013b20462e76a22049398b2ef378a71e15ce19a45940b7e617c314" {
		t.Fatalf("idempotency key = %q", identity.Key)
	}
	if err := identity.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDeriveIdentityCanonicalizesArgumentObjectOrder(t *testing.T) {
	base := DerivationInput{
		RunContext: domain.RunContext{
			RunID:       "run-identity",
			IncidentID:  "incident-identity",
			WaybillID:   "YD2026101001",
			PlanVersion: 1,
		},
		Action:    domain.ActionSendSMS,
		Target:    "phone/13800001234",
		Arguments: []byte(`{"phone":"13800001234","template_id":"delay","params":{"b":"2","a":"1"}}`),
	}
	first, err := Derive(base)
	if err != nil {
		t.Fatal(err)
	}
	base.Arguments = []byte(`{"params":{"a":"1","b":"2"},"template_id":"delay","phone":"13800001234"}`)
	second, err := Derive(base)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("identities differ:\nfirst:  %+v\nsecond: %+v", first, second)
	}
}

func TestDeriveIdentityChangesWithBusinessIdentity(t *testing.T) {
	base := DerivationInput{
		RunContext: domain.RunContext{
			RunID:       "run-identity",
			IncidentID:  "incident-identity",
			WaybillID:   "YD2026101001",
			PlanVersion: 1,
		},
		Action:    domain.ActionSendSMS,
		Target:    "phone/13800001234",
		Arguments: []byte(`{"phone":"13800001234","template_id":"delay","params":{}}`),
	}
	first, err := Derive(base)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*DerivationInput)
	}{
		{"run", func(value *DerivationInput) { value.RunContext.RunID = "other-run" }},
		{"plan", func(value *DerivationInput) { value.RunContext.PlanVersion = 2 }},
		{"action", func(value *DerivationInput) { value.Action = domain.ActionCreateClaim }},
		{"target", func(value *DerivationInput) { value.Target = "phone/13900005678" }},
		{"arguments", func(value *DerivationInput) {
			value.Arguments = []byte(`{"phone":"13800001234","template_id":"other","params":{}}`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			test.mutate(&changed)
			identity, err := Derive(changed)
			if err != nil {
				t.Fatal(err)
			}
			if identity.EffectID == first.EffectID || identity.Key == first.Key {
				t.Fatalf("identity did not change: %+v", identity)
			}
		})
	}
}

func TestEffectV0IdentityMatchesMergedMainAlgorithm(t *testing.T) {
	identity, err := EffectV0Identity(domain.RunContext{
		RunID:       "run-effects",
		IncidentID:  "incident-effects",
		WaybillID:   "YD2026101001",
		PlanVersion: 1,
	}, domain.ActionSendSMS, "args-hash")
	if err != nil {
		t.Fatal(err)
	}
	if identity.EffectID != "38e4192b-48be-524e-9bc8-20c8cae9f245" {
		t.Fatalf("effect id = %q", identity.EffectID)
	}
	if identity.Key != "14bb3f30392aa4eee584e394b9bab028f397e78d389614e9c00b54e383db291f" {
		t.Fatalf("key = %q", identity.Key)
	}
	if err := identity.Validate(); err != nil {
		t.Fatal(err)
	}
}
