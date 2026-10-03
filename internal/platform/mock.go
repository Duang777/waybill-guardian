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

type DemoData struct {
	Waybills map[string]Waybill       `json:"waybills"`
	Tracking map[string][]TrackPoint  `json:"tracking"`
	Drivers  map[string]Driver        `json:"drivers"`
	Weather  map[string][]RoadWeather `json:"weather"`
}

type Mock struct {
	mu sync.Mutex

	data DemoData

	reassignments map[domain.IdempotencyKey]ReassignOrder
	claims        map[domain.IdempotencyKey]ClaimOrder
	messages      map[domain.IdempotencyKey]SMSReceipt
	writeCalls    map[domain.Action]int
}

func NewMock(raw []byte) (*Mock, error) {
	var data DemoData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("decode demo data: %w", err)
	}
	if len(data.Waybills) == 0 {
		return nil, fmt.Errorf("demo data has no waybills")
	}
	return &Mock{
		data:          data,
		reassignments: make(map[domain.IdempotencyKey]ReassignOrder),
		claims:        make(map[domain.IdempotencyKey]ClaimOrder),
		messages:      make(map[domain.IdempotencyKey]SMSReceipt),
		writeCalls:    make(map[domain.Action]int),
	}, nil
}

func (m *Mock) GetWaybill(_ context.Context, req GetWaybillRequest) (Waybill, error) {
	waybill, ok := m.data.Waybills[string(req.WaybillID)]
	if !ok {
		return Waybill{}, fmt.Errorf("waybill %q not found", req.WaybillID)
	}
	return cloneWaybill(waybill), nil
}

func (m *Mock) GetTracking(_ context.Context, req GetTrackingRequest) ([]TrackPoint, error) {
	points, ok := m.data.Tracking[string(req.WaybillID)]
	if !ok {
		return nil, fmt.Errorf("tracking for waybill %q not found", req.WaybillID)
	}
	return append([]TrackPoint(nil), points...), nil
}

func (m *Mock) GetDriver(_ context.Context, req GetDriverRequest) (Driver, error) {
	driver, ok := m.data.Drivers[string(req.DriverID)]
	if !ok {
		return Driver{}, fmt.Errorf("driver %q not found", req.DriverID)
	}
	return driver, nil
}

func (m *Mock) GetRoadWeather(_ context.Context, req GetRoadWeatherRequest) ([]RoadWeather, error) {
	weather, ok := m.data.Weather[req.Route]
	if !ok {
		return nil, fmt.Errorf("weather for route %q not found", req.Route)
	}
	return append([]RoadWeather(nil), weather...), nil
}

func (m *Mock) Reassign(_ context.Context, req ReassignRequest) (ReassignOrder, error) {
	if req.IdempotencyKey == "" {
		return ReassignOrder{}, fmt.Errorf("idempotency key is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if result, ok := m.reassignments[req.IdempotencyKey]; ok {
		return result, nil
	}
	waybill, ok := m.data.Waybills[string(req.WaybillID)]
	if !ok {
		return ReassignOrder{}, fmt.Errorf("waybill %q not found", req.WaybillID)
	}
	if !containsCarrier(waybill.CandidateCarriers, req.CarrierID) {
		return ReassignOrder{}, fmt.Errorf("carrier %q is not a candidate", req.CarrierID)
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

func (m *Mock) CreateClaim(_ context.Context, req CreateClaimRequest) (ClaimOrder, error) {
	if req.IdempotencyKey == "" {
		return ClaimOrder{}, fmt.Errorf("idempotency key is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if result, ok := m.claims[req.IdempotencyKey]; ok {
		return result, nil
	}
	if _, ok := m.data.Waybills[string(req.WaybillID)]; !ok {
		return ClaimOrder{}, fmt.Errorf("waybill %q not found", req.WaybillID)
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

func (m *Mock) WriteCount(action domain.Action) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writeCalls[action]
}

func cloneWaybill(in Waybill) Waybill {
	out := in
	out.CandidateCarriers = append([]Carrier(nil), in.CandidateCarriers...)
	return out
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
