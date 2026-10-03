package tools

import (
	"context"
	_ "embed"
	"fmt"
	"regexp"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform"
)

var waybillIDPattern = regexp.MustCompile(`^YD[0-9]{10}$`)

//go:embed testdata/demo.json
var demoData []byte

func NewDemoClients() (platform.Clients, *platform.Mock, error) {
	mock, err := platform.NewMock(demoData)
	if err != nil {
		return platform.Clients{}, nil, err
	}
	return platform.Clients{TMS: mock, Weather: mock, Notification: mock}, mock, nil
}

type GetWaybillInput struct {
	WaybillID string `json:"waybill_id" jsonschema_description:"Waybill identifier"`
}

type GetWaybillOutput = platform.Waybill

type GetTrackingInput struct {
	WaybillID string `json:"waybill_id" jsonschema_description:"Waybill identifier"`
}

type GetTrackingOutput struct {
	Points []platform.TrackPoint `json:"points"`
}

type GetDriverInput struct {
	DriverID string `json:"driver_id" jsonschema_description:"Driver identifier"`
}

type GetDriverOutput = platform.Driver

type GetRoadWeatherInput struct {
	Route string `json:"route" jsonschema_description:"Route in origin-destination form"`
}

type GetRoadWeatherOutput struct {
	Segments []platform.RoadWeather `json:"segments"`
}

type ReassignInput struct {
	WaybillID string `json:"waybill_id" jsonschema_description:"Waybill identifier"`
	CarrierID string `json:"carrier_id" jsonschema_description:"Target carrier identifier"`
}

type ReassignOutput = platform.ReassignOrder

type CreateClaimInput struct {
	WaybillID string `json:"waybill_id" jsonschema_description:"Waybill identifier"`
	ClaimType string `json:"claim_type" jsonschema_description:"Claim category"`
}

type CreateClaimOutput = platform.ClaimOrder

type SendSMSInput struct {
	Phone      string            `json:"phone" jsonschema_description:"Destination mobile number"`
	TemplateID string            `json:"template_id" jsonschema_description:"Notification template identifier"`
	Params     map[string]string `json:"params" jsonschema_description:"Template variables"`
}

type SendSMSOutput = platform.SMSReceipt

type Handlers struct {
	clients platform.Clients
}

type idempotencyKeyContextKey struct{}

func WithIdempotencyKey(ctx context.Context, key domain.IdempotencyKey) context.Context {
	return context.WithValue(ctx, idempotencyKeyContextKey{}, key)
}

func idempotencyKeyFromContext(ctx context.Context) (domain.IdempotencyKey, error) {
	key, _ := ctx.Value(idempotencyKeyContextKey{}).(domain.IdempotencyKey)
	if key == "" {
		return "", fmt.Errorf("server-generated idempotency key is required")
	}
	return key, nil
}

func NewHandlers(clients platform.Clients) (*Handlers, error) {
	if clients.TMS == nil || clients.Weather == nil || clients.Notification == nil {
		return nil, fmt.Errorf("all platform clients are required")
	}
	return &Handlers{clients: clients}, nil
}

func (h *Handlers) GetWaybill(ctx context.Context, in GetWaybillInput) (GetWaybillOutput, error) {
	if err := validateWaybillID(in.WaybillID); err != nil {
		return GetWaybillOutput{}, err
	}
	return h.clients.TMS.GetWaybill(ctx, platform.GetWaybillRequest{
		WaybillID: domain.WaybillID(in.WaybillID),
	})
}

func (h *Handlers) GetTracking(ctx context.Context, in GetTrackingInput) (GetTrackingOutput, error) {
	if err := validateWaybillID(in.WaybillID); err != nil {
		return GetTrackingOutput{}, err
	}
	points, err := h.clients.TMS.GetTracking(ctx, platform.GetTrackingRequest{
		WaybillID: domain.WaybillID(in.WaybillID),
	})
	if err != nil {
		return GetTrackingOutput{}, err
	}
	return GetTrackingOutput{Points: points}, nil
}

func (h *Handlers) GetDriver(ctx context.Context, in GetDriverInput) (GetDriverOutput, error) {
	if in.DriverID == "" {
		return GetDriverOutput{}, fmt.Errorf("driver_id is required")
	}
	return h.clients.TMS.GetDriver(ctx, platform.GetDriverRequest{
		DriverID: domain.DriverID(in.DriverID),
	})
}

func (h *Handlers) GetRoadWeather(ctx context.Context, in GetRoadWeatherInput) (GetRoadWeatherOutput, error) {
	if in.Route == "" {
		return GetRoadWeatherOutput{}, fmt.Errorf("route is required")
	}
	segments, err := h.clients.Weather.GetRoadWeather(ctx, platform.GetRoadWeatherRequest{Route: in.Route})
	if err != nil {
		return GetRoadWeatherOutput{}, err
	}
	return GetRoadWeatherOutput{Segments: segments}, nil
}

func (h *Handlers) Reassign(ctx context.Context, in ReassignInput) (ReassignOutput, error) {
	if err := validateWaybillID(in.WaybillID); err != nil {
		return ReassignOutput{}, err
	}
	if in.CarrierID == "" {
		return ReassignOutput{}, fmt.Errorf("carrier_id is required")
	}
	key, err := idempotencyKeyFromContext(ctx)
	if err != nil {
		return ReassignOutput{}, err
	}
	return h.clients.TMS.Reassign(ctx, platform.ReassignRequest{
		WaybillID:      domain.WaybillID(in.WaybillID),
		CarrierID:      domain.CarrierID(in.CarrierID),
		IdempotencyKey: key,
	})
}

func (h *Handlers) CreateClaim(ctx context.Context, in CreateClaimInput) (CreateClaimOutput, error) {
	if err := validateWaybillID(in.WaybillID); err != nil {
		return CreateClaimOutput{}, err
	}
	if in.ClaimType == "" {
		return CreateClaimOutput{}, fmt.Errorf("claim_type is required")
	}
	key, err := idempotencyKeyFromContext(ctx)
	if err != nil {
		return CreateClaimOutput{}, err
	}
	return h.clients.TMS.CreateClaim(ctx, platform.CreateClaimRequest{
		WaybillID:      domain.WaybillID(in.WaybillID),
		ClaimType:      in.ClaimType,
		IdempotencyKey: key,
	})
}

func (h *Handlers) SendSMS(ctx context.Context, in SendSMSInput) (SendSMSOutput, error) {
	if in.Phone == "" || in.TemplateID == "" {
		return SendSMSOutput{}, fmt.Errorf("phone and template_id are required")
	}
	if in.Params == nil {
		return SendSMSOutput{}, fmt.Errorf("params is required")
	}
	key, err := idempotencyKeyFromContext(ctx)
	if err != nil {
		return SendSMSOutput{}, err
	}
	return h.clients.Notification.SendSMS(ctx, platform.SendSMSRequest{
		Phone:          in.Phone,
		TemplateID:     in.TemplateID,
		Params:         in.Params,
		IdempotencyKey: key,
	})
}

func validateWaybillID(id string) error {
	if !waybillIDPattern.MatchString(id) {
		return fmt.Errorf("waybill_id must match YD followed by 10 digits")
	}
	return nil
}
