package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform"
)

const (
	FixtureRuntimeAdapterID       = "fixture-v1"
	fixtureRuntimeContractVersion = "v1"
	fixtureRuntimeKeyRetention    = 10 * 365 * 24 * time.Hour
)

type FixtureWriteRuntime struct {
	clients platform.Clients
}

func NewFixtureWriteRuntime(clients platform.Clients) (*FixtureWriteRuntime, error) {
	if clients.TMS == nil || clients.Notification == nil {
		return nil, platform.ErrNotImplemented
	}
	return &FixtureWriteRuntime{clients: clients}, nil
}

func (r *FixtureWriteRuntime) AdvertisedActions() []domain.Action {
	return []domain.Action{
		domain.ActionReassign,
		domain.ActionCreateClaim,
		domain.ActionSendSMS,
	}
}

func (r *FixtureWriteRuntime) Bind(
	request platform.EffectRequest,
	key domain.IdempotencyKey,
	createdAt time.Time,
) (platform.EffectBinding, error) {
	if key == "" || createdAt.IsZero() || !fixtureAction(request.Action) {
		return platform.EffectBinding{}, platform.ErrNotImplemented
	}
	if _, err := r.validateRequest(request); err != nil {
		return platform.EffectBinding{}, err
	}
	createdAt = createdAt.UTC()
	return platform.EffectBinding{
		SchemaVersion:           1,
		Action:                  request.Action,
		AdapterID:               FixtureRuntimeAdapterID,
		ContractVersion:         fixtureRuntimeContractVersion,
		ProviderOperation:       string(request.Action),
		ProviderScopeDigest:     fixtureDigest([]byte(FixtureRuntimeAdapterID)),
		ProviderRequestHash:     fixtureDigest(request.Arguments),
		KeyCreatedAt:            createdAt,
		KeyExpiresAt:            createdAt.Add(fixtureRuntimeKeyRetention),
		LookupConsistencyWindow: 0,
	}, nil
}

func (r *FixtureWriteRuntime) Dispatch(
	ctx context.Context,
	binding platform.EffectBinding,
	request platform.EffectRequest,
	key domain.IdempotencyKey,
) platform.DispatchResult {
	if key == "" || !r.SupportsRecovery(binding) ||
		binding.Action != request.Action ||
		binding.ProviderRequestHash != fixtureDigest(request.Arguments) {
		return platform.DispatchResult{
			Disposition: platform.EffectPermanentFailed,
			ErrorCode:   "binding_conflict",
		}
	}
	input, err := r.validateRequest(request)
	if err != nil {
		return platform.DispatchResult{
			Disposition: platform.EffectPermanentFailed,
			ErrorCode:   "invalid_request",
		}
	}

	var value any
	switch typed := input.(type) {
	case ReassignInput:
		value, err = r.clients.TMS.Reassign(ctx, platform.ReassignRequest{
			WaybillID:      domain.WaybillID(typed.WaybillID),
			CarrierID:      domain.CarrierID(typed.CarrierID),
			IdempotencyKey: key,
		})
	case CreateClaimInput:
		value, err = r.clients.TMS.CreateClaim(ctx, platform.CreateClaimRequest{
			WaybillID:      domain.WaybillID(typed.WaybillID),
			ClaimType:      typed.ClaimType,
			IdempotencyKey: key,
		})
	case SendSMSInput:
		value, err = r.sendSMS(ctx, typed, key)
	}
	if err != nil {
		return platform.DispatchResult{
			Disposition: fixtureErrorDisposition(err),
			ErrorCode:   "fixture_dispatch_failed",
		}
	}
	response, err := json.Marshal(value)
	if err != nil {
		return platform.DispatchResult{
			Disposition: platform.EffectUnknown,
			ErrorCode:   "invalid_response",
		}
	}
	return platform.DispatchResult{
		Disposition:    platform.EffectSucceeded,
		Response:       response,
		ResponseDigest: fixtureDigest(response),
	}
}

func (r *FixtureWriteRuntime) Lookup(
	ctx context.Context,
	binding platform.EffectBinding,
	key domain.IdempotencyKey,
) platform.LookupResult {
	if key == "" || !r.SupportsRecovery(binding) {
		return platform.LookupResult{
			Disposition: platform.LookupConflict,
			ErrorCode:   "binding_conflict",
		}
	}
	request := platform.LookupEffectRequest{
		Action:         binding.Action,
		IdempotencyKey: key,
	}
	var effect platform.EffectResult
	var err error
	switch binding.Action {
	case domain.ActionReassign, domain.ActionCreateClaim:
		effect, err = r.clients.TMS.LookupEffect(ctx, request)
	case domain.ActionSendSMS:
		effect, err = r.clients.Notification.LookupEffect(ctx, request)
	}
	if err != nil {
		return platform.LookupResult{
			Disposition: platform.LookupPending,
			ErrorCode:   "fixture_lookup_failed",
		}
	}
	result := platform.LookupResult{
		Response:    append(json.RawMessage(nil), effect.Response...),
		ExternalRef: effect.ExternalRef,
		RetryAfter:  effect.RetryAfter,
	}
	if len(effect.Response) > 0 {
		result.ResponseDigest = fixtureDigest(effect.Response)
	}
	switch effect.Disposition {
	case platform.EffectSucceeded:
		result.Disposition = platform.LookupApplied
	case platform.EffectRetryableFailed:
		result.Disposition = platform.LookupAbsent
	case platform.EffectPermanentFailed:
		result.Disposition = platform.LookupRejected
	default:
		result.Disposition = platform.LookupPending
		result.ErrorCode = "fixture_lookup_pending"
	}
	return result
}

func (r *FixtureWriteRuntime) SupportsRecovery(binding platform.EffectBinding) bool {
	return binding.SchemaVersion == 1 &&
		fixtureAction(binding.Action) &&
		binding.AdapterID == FixtureRuntimeAdapterID &&
		binding.ContractVersion == fixtureRuntimeContractVersion &&
		binding.ProviderOperation == string(binding.Action) &&
		binding.ProviderScopeDigest == fixtureDigest([]byte(FixtureRuntimeAdapterID)) &&
		validFixtureDigest(binding.ProviderRequestHash) &&
		!binding.KeyCreatedAt.IsZero() &&
		binding.KeyExpiresAt.Equal(binding.KeyCreatedAt.Add(fixtureRuntimeKeyRetention)) &&
		binding.LookupConsistencyWindow == 0
}

func fixtureErrorDisposition(err error) platform.EffectDisposition {
	var effectErr *platform.EffectError
	if errors.As(err, &effectErr) {
		return platform.EffectDispositionOf(err)
	}
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return platform.EffectUnknown
	}
	return platform.EffectPermanentFailed
}

func validFixtureDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (r *FixtureWriteRuntime) validateRequest(
	request platform.EffectRequest,
) (any, error) {
	switch request.Action {
	case domain.ActionReassign:
		var input ReassignInput
		if err := decodeStrict(request.Arguments, &input); err != nil {
			return nil, err
		}
		return input, validateReassign(input)
	case domain.ActionCreateClaim:
		var input CreateClaimInput
		if err := decodeStrict(request.Arguments, &input); err != nil {
			return nil, err
		}
		return input, validateCreateClaim(input)
	case domain.ActionSendSMS:
		var input SendSMSInput
		if err := decodeStrict(request.Arguments, &input); err != nil {
			return nil, err
		}
		return input, validateSendSMS(input)
	default:
		return nil, platform.ErrNotImplemented
	}
}

func (r *FixtureWriteRuntime) sendSMS(
	ctx context.Context,
	input SendSMSInput,
	key domain.IdempotencyKey,
) (platform.SMSReceipt, error) {
	waybill, err := r.clients.TMS.GetWaybill(ctx, platform.GetWaybillRequest{
		WaybillID: domain.WaybillID(input.WaybillID),
	})
	if err != nil {
		return platform.SMSReceipt{}, err
	}
	phone := waybill.ShipperPhone
	templateID := "waybill_reassigned"
	if input.Recipient == RecipientDriver {
		driver, err := r.clients.TMS.GetDriver(ctx, platform.GetDriverRequest{
			DriverID: waybill.DriverID,
		})
		if err != nil {
			return platform.SMSReceipt{}, err
		}
		phone = driver.Phone
		templateID = "waybill_reassigned_driver"
	}
	return r.clients.Notification.SendSMS(ctx, platform.SendSMSRequest{
		Phone:      phone,
		TemplateID: templateID,
		Params: map[string]string{
			"waybill_id": input.WaybillID,
			"carrier_id": input.CarrierID,
		},
		IdempotencyKey: key,
	})
}

func fixtureAction(action domain.Action) bool {
	switch action {
	case domain.ActionReassign, domain.ActionCreateClaim, domain.ActionSendSMS:
		return true
	default:
		return false
	}
}

func fixtureDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

var _ platform.WriteRuntime = (*FixtureWriteRuntime)(nil)
