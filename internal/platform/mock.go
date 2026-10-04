package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/Duang777/waybill-guardian/internal/domain"
)

type Mock struct {
	mu sync.Mutex

	reads ReadSet

	reassignments map[domain.IdempotencyKey]ReassignOrder
	claims        map[domain.IdempotencyKey]ClaimOrder
	messages      map[domain.IdempotencyKey]SMSReceipt
	writeCalls    map[domain.Action]int
}

func NewMock(reads ReadSet) (*Mock, error) {
	if reads.TMS == nil || reads.Weather == nil || reads.Catalog == nil {
		return nil, fmt.Errorf("fixture read set is incomplete")
	}
	return &Mock{
		reads:         reads,
		reassignments: make(map[domain.IdempotencyKey]ReassignOrder),
		claims:        make(map[domain.IdempotencyKey]ClaimOrder),
		messages:      make(map[domain.IdempotencyKey]SMSReceipt),
		writeCalls:    make(map[domain.Action]int),
	}, nil
}

func (m *Mock) GetWaybill(ctx context.Context, req GetWaybillRequest) (Waybill, error) {
	return m.reads.TMS.GetWaybill(ctx, req)
}

func (m *Mock) GetTracking(ctx context.Context, req GetTrackingRequest) ([]TrackPoint, error) {
	return m.reads.TMS.GetTracking(ctx, req)
}

func (m *Mock) GetDriver(ctx context.Context, req GetDriverRequest) (Driver, error) {
	return m.reads.TMS.GetDriver(ctx, req)
}

func (m *Mock) GetRoadWeather(
	ctx context.Context,
	req GetRoadWeatherRequest,
) ([]RoadWeather, error) {
	return m.reads.Weather.GetRoadWeather(ctx, req)
}

func (m *Mock) Reassign(ctx context.Context, req ReassignRequest) (ReassignOrder, error) {
	if req.IdempotencyKey == "" {
		return ReassignOrder{}, fmt.Errorf("idempotency key is required")
	}
	waybill, err := m.reads.TMS.GetWaybill(ctx, GetWaybillRequest{
		WaybillID: req.WaybillID,
	})
	if err != nil {
		return ReassignOrder{}, err
	}
	if !containsCarrier(waybill.CandidateCarriers, req.CarrierID) {
		return ReassignOrder{}, fmt.Errorf("carrier %q is not a candidate", req.CarrierID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if result, ok := m.reassignments[req.IdempotencyKey]; ok {
		return result, nil
	}
	result := ReassignOrder{
		OrderID:   stableID("RA", req.IdempotencyKey),
		WaybillID: req.WaybillID,
		CarrierID: req.CarrierID,
		Status:    "accepted",
	}
	m.reassignments[req.IdempotencyKey] = result
	m.writeCalls[domain.ActionReassign]++
	return result, nil
}

func (m *Mock) CreateClaim(
	ctx context.Context,
	req CreateClaimRequest,
) (ClaimOrder, error) {
	if req.IdempotencyKey == "" {
		return ClaimOrder{}, fmt.Errorf("idempotency key is required")
	}
	if _, err := m.reads.TMS.GetWaybill(ctx, GetWaybillRequest{
		WaybillID: req.WaybillID,
	}); err != nil {
		return ClaimOrder{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if result, ok := m.claims[req.IdempotencyKey]; ok {
		return result, nil
	}
	result := ClaimOrder{
		ClaimID:   stableID("CL", req.IdempotencyKey),
		WaybillID: req.WaybillID,
		ClaimType: req.ClaimType,
		Status:    "created",
	}
	m.claims[req.IdempotencyKey] = result
	m.writeCalls[domain.ActionCreateClaim]++
	return result, nil
}

func (m *Mock) SendSMS(_ context.Context, req SendSMSRequest) (SMSReceipt, error) {
	if req.IdempotencyKey == "" {
		return SMSReceipt{}, fmt.Errorf("idempotency key is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if result, ok := m.messages[req.IdempotencyKey]; ok {
		return result, nil
	}
	if req.Phone == "" || req.TemplateID == "" {
		return SMSReceipt{}, fmt.Errorf("phone and template_id are required")
	}
	result := SMSReceipt{MessageID: stableID("SMS", req.IdempotencyKey), Status: "mock_sent"}
	m.messages[req.IdempotencyKey] = result
	m.writeCalls[domain.ActionSendSMS]++
	return result, nil
}

func (m *Mock) LookupEffect(_ context.Context, req LookupEffectRequest) (EffectResult, error) {
	if req.IdempotencyKey == "" {
		return EffectResult{Disposition: EffectPermanentFailed}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	var value any
	switch req.Action {
	case domain.ActionReassign:
		result, ok := m.reassignments[req.IdempotencyKey]
		if !ok {
			return EffectResult{Disposition: EffectRetryableFailed}, nil
		}
		value = result
	case domain.ActionCreateClaim:
		result, ok := m.claims[req.IdempotencyKey]
		if !ok {
			return EffectResult{Disposition: EffectRetryableFailed}, nil
		}
		value = result
	case domain.ActionSendSMS:
		result, ok := m.messages[req.IdempotencyKey]
		if !ok {
			return EffectResult{Disposition: EffectRetryableFailed}, nil
		}
		value = result
	default:
		return EffectResult{Disposition: EffectPermanentFailed}, nil
	}
	response, err := json.Marshal(value)
	if err != nil {
		return EffectResult{Disposition: EffectUnknown}, err
	}
	return EffectResult{
		Disposition: EffectSucceeded,
		Response:    response,
	}, nil
}

func (m *Mock) WriteCount(action domain.Action) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writeCalls[action]
}

func containsCarrier(carriers []Carrier, id domain.CarrierID) bool {
	for _, carrier := range carriers {
		if carrier.ID == id {
			return true
		}
	}
	return false
}

func stableID(prefix string, key domain.IdempotencyKey) string {
	sum := sha256.Sum256([]byte(key))
	return prefix + "-" + hex.EncodeToString(sum[:6])
}

var (
	_ TMSClient          = (*Mock)(nil)
	_ WeatherClient      = (*Mock)(nil)
	_ NotificationClient = (*Mock)(nil)
)
