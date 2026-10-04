package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
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
	reads       platform.ReadSet
	scopeDigest string

	mu            sync.Mutex
	reassignments map[domain.IdempotencyKey]platform.ReassignOrder
	claims        map[domain.IdempotencyKey]platform.ClaimOrder
	messages      map[domain.IdempotencyKey]platform.SMSReceipt
	writeCalls    map[domain.Action]int
}

func NewFixtureWriteRuntime(reads platform.ReadSet) (*FixtureWriteRuntime, error) {
	return NewFixtureWriteRuntimeForSource(reads, FixtureRuntimeAdapterID)
}

func NewFixtureWriteRuntimeForSource(
	reads platform.ReadSet,
	sourceIdentity string,
) (*FixtureWriteRuntime, error) {
	if reads.TMS == nil || reads.Weather == nil || reads.Catalog == nil {
		return nil, fmt.Errorf("fixture read set is incomplete")
	}
	if strings.TrimSpace(sourceIdentity) == "" {
		return nil, fmt.Errorf("fixture source identity is required")
	}
	return &FixtureWriteRuntime{
		reads:         reads,
		scopeDigest:   fixtureDigest([]byte(sourceIdentity)),
		reassignments: make(map[domain.IdempotencyKey]platform.ReassignOrder),
		claims:        make(map[domain.IdempotencyKey]platform.ClaimOrder),
		messages:      make(map[domain.IdempotencyKey]platform.SMSReceipt),
		writeCalls:    make(map[domain.Action]int),
	}, nil
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
		ProviderScopeDigest:     r.scopeDigest,
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
		value, err = r.reassign(ctx, typed, key)
	case CreateClaimInput:
		value, err = r.createClaim(ctx, typed, key)
	case SendSMSInput:
		value, err = r.sendSMS(ctx, typed, key)
	}
	if err != nil {
		disposition := platform.EffectPermanentFailed
		if errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			disposition = platform.EffectUnknown
		}
		return platform.DispatchResult{
			Disposition: disposition,
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
	_ context.Context,
	binding platform.EffectBinding,
	key domain.IdempotencyKey,
) platform.LookupResult {
	if key == "" || !r.SupportsRecovery(binding) {
		return platform.LookupResult{
			Disposition: platform.LookupConflict,
			ErrorCode:   "binding_conflict",
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	var value any
	switch binding.Action {
	case domain.ActionReassign:
		result, ok := r.reassignments[key]
		if !ok {
			return platform.LookupResult{Disposition: platform.LookupAbsent}
		}
		value = result
	case domain.ActionCreateClaim:
		result, ok := r.claims[key]
		if !ok {
			return platform.LookupResult{Disposition: platform.LookupAbsent}
		}
		value = result
	case domain.ActionSendSMS:
		result, ok := r.messages[key]
		if !ok {
			return platform.LookupResult{Disposition: platform.LookupAbsent}
		}
		value = result
	default:
		return platform.LookupResult{Disposition: platform.LookupRejected}
	}
	response, err := json.Marshal(value)
	if err != nil {
		return platform.LookupResult{
			Disposition: platform.LookupPending,
			ErrorCode:   "invalid_response",
		}
	}
	return platform.LookupResult{
		Disposition:    platform.LookupApplied,
		Response:       response,
		ResponseDigest: fixtureDigest(response),
	}
}

func (r *FixtureWriteRuntime) SupportsRecovery(binding platform.EffectBinding) bool {
	return binding.SchemaVersion == 1 &&
		fixtureAction(binding.Action) &&
		binding.AdapterID == FixtureRuntimeAdapterID &&
		binding.ContractVersion == fixtureRuntimeContractVersion &&
		binding.ProviderOperation == string(binding.Action) &&
		binding.ProviderScopeDigest == r.scopeDigest &&
		validFixtureDigest(binding.ProviderRequestHash) &&
		!binding.KeyCreatedAt.IsZero() &&
		binding.KeyExpiresAt.Equal(binding.KeyCreatedAt.Add(fixtureRuntimeKeyRetention)) &&
		binding.LookupConsistencyWindow == 0
}

func (r *FixtureWriteRuntime) WriteCount(action domain.Action) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writeCalls[action]
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

func (r *FixtureWriteRuntime) reassign(
	ctx context.Context,
	input ReassignInput,
	key domain.IdempotencyKey,
) (platform.ReassignOrder, error) {
	waybill, err := r.reads.TMS.GetWaybill(ctx, platform.GetWaybillRequest{
		WaybillID: domain.WaybillID(input.WaybillID),
	})
	if err != nil {
		return platform.ReassignOrder{}, err
	}
	carrierID := domain.CarrierID(input.CarrierID)
	if !containsCarrier(waybill.CandidateCarriers, carrierID) {
		return platform.ReassignOrder{}, fmt.Errorf(
			"carrier %q is not a candidate",
			carrierID,
		)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if result, ok := r.reassignments[key]; ok {
		return result, nil
	}
	result := platform.ReassignOrder{
		OrderID:   fixtureStableID("RA", key),
		WaybillID: waybill.ID,
		CarrierID: carrierID,
		Status:    "accepted",
	}
	r.reassignments[key] = result
	r.writeCalls[domain.ActionReassign]++
	return result, nil
}

func (r *FixtureWriteRuntime) createClaim(
	ctx context.Context,
	input CreateClaimInput,
	key domain.IdempotencyKey,
) (platform.ClaimOrder, error) {
	waybillID := domain.WaybillID(input.WaybillID)
	if _, err := r.reads.TMS.GetWaybill(ctx, platform.GetWaybillRequest{
		WaybillID: waybillID,
	}); err != nil {
		return platform.ClaimOrder{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if result, ok := r.claims[key]; ok {
		return result, nil
	}
	result := platform.ClaimOrder{
		ClaimID:   fixtureStableID("CL", key),
		WaybillID: waybillID,
		ClaimType: input.ClaimType,
		Status:    "created",
	}
	r.claims[key] = result
	r.writeCalls[domain.ActionCreateClaim]++
	return result, nil
}

func (r *FixtureWriteRuntime) sendSMS(
	ctx context.Context,
	input SendSMSInput,
	key domain.IdempotencyKey,
) (platform.SMSReceipt, error) {
	waybill, err := r.reads.TMS.GetWaybill(ctx, platform.GetWaybillRequest{
		WaybillID: domain.WaybillID(input.WaybillID),
	})
	if err != nil {
		return platform.SMSReceipt{}, err
	}
	if !containsCarrier(
		waybill.CandidateCarriers,
		domain.CarrierID(input.CarrierID),
	) {
		return platform.SMSReceipt{}, fmt.Errorf(
			"carrier %q is not a candidate",
			input.CarrierID,
		)
	}
	if input.Recipient == RecipientDriver {
		if _, err := r.reads.TMS.GetDriver(ctx, platform.GetDriverRequest{
			DriverID: waybill.DriverID,
		}); err != nil {
			return platform.SMSReceipt{}, err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if result, ok := r.messages[key]; ok {
		return result, nil
	}
	result := platform.SMSReceipt{
		MessageID: fixtureStableID("SMS", key),
		Status:    "mock_sent",
	}
	r.messages[key] = result
	r.writeCalls[domain.ActionSendSMS]++
	return result, nil
}

func containsCarrier(
	carriers []platform.Carrier,
	id domain.CarrierID,
) bool {
	for _, carrier := range carriers {
		if carrier.ID == id {
			return true
		}
	}
	return false
}

func fixtureAction(action domain.Action) bool {
	switch action {
	case domain.ActionReassign, domain.ActionCreateClaim, domain.ActionSendSMS:
		return true
	default:
		return false
	}
}

func validFixtureDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func fixtureDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func fixtureStableID(prefix string, key domain.IdempotencyKey) string {
	sum := sha256.Sum256([]byte(key))
	return prefix + "-" + hex.EncodeToString(sum[:6])
}

var _ platform.WriteRuntime = (*FixtureWriteRuntime)(nil)
