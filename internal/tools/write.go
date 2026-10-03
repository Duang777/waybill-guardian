package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/idempotency"
)

type CanonicalWrite struct {
	Action         domain.Action
	WireName       string
	Target         string
	Arguments      json.RawMessage
	ArgumentsHash  string
	LegacyKey      domain.IdempotencyKey
	LegacyFullHash string
}

type legacyReassignInput struct {
	ReassignInput
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type legacyCreateClaimInput struct {
	CreateClaimInput
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type legacySendSMSInput struct {
	SendSMSInput
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

func (r *Registry) ParseWrite(wireName string, raw json.RawMessage) (CanonicalWrite, error) {
	definition, ok := r.ByWireName(wireName)
	if !ok || definition.Access != AccessWrite {
		return CanonicalWrite{}, fmt.Errorf("write tool %q is not registered", wireName)
	}
	fullHash, err := idempotency.ArgumentsHash(string(raw))
	if err != nil {
		return CanonicalWrite{}, err
	}

	var arguments any
	var target, legacyKey string
	switch definition.Action {
	case domain.ActionReassign:
		var input legacyReassignInput
		if decodeErr := decodeStrict(raw, &input); decodeErr != nil {
			return CanonicalWrite{}, decodeErr
		}
		if validationErr := validateReassign(input.ReassignInput); validationErr != nil {
			return CanonicalWrite{}, validationErr
		}
		arguments = input.ReassignInput
		target = "waybill/" + input.WaybillID + "/carrier/" + input.CarrierID
		legacyKey = input.IdempotencyKey
	case domain.ActionCreateClaim:
		var input legacyCreateClaimInput
		if decodeErr := decodeStrict(raw, &input); decodeErr != nil {
			return CanonicalWrite{}, decodeErr
		}
		if validationErr := validateCreateClaim(input.CreateClaimInput); validationErr != nil {
			return CanonicalWrite{}, validationErr
		}
		arguments = input.CreateClaimInput
		target = "waybill/" + input.WaybillID + "/claim/" + input.ClaimType
		legacyKey = input.IdempotencyKey
	case domain.ActionSendSMS:
		var input legacySendSMSInput
		if decodeErr := decodeStrict(raw, &input); decodeErr != nil {
			return CanonicalWrite{}, decodeErr
		}
		if validationErr := validateSendSMS(input.SendSMSInput); validationErr != nil {
			return CanonicalWrite{}, validationErr
		}
		arguments = input.SendSMSInput
		target = "phone/" + input.Phone
		legacyKey = input.IdempotencyKey
	default:
		return CanonicalWrite{}, fmt.Errorf("action %q is not a write tool", definition.Action)
	}

	canonical, err := json.Marshal(arguments)
	if err != nil {
		return CanonicalWrite{}, fmt.Errorf("marshal canonical write arguments: %w", err)
	}
	argumentsHash, err := idempotency.ArgumentsHash(string(canonical))
	if err != nil {
		return CanonicalWrite{}, err
	}
	return CanonicalWrite{
		Action:         definition.Action,
		WireName:       definition.WireName,
		Target:         target,
		Arguments:      canonical,
		ArgumentsHash:  argumentsHash,
		LegacyKey:      domain.IdempotencyKey(legacyKey),
		LegacyFullHash: fullHash,
	}, nil
}

func decodeStrict(raw json.RawMessage, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode write arguments: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode write arguments: multiple JSON values")
		}
		return fmt.Errorf("decode write arguments: %w", err)
	}
	return nil
}
