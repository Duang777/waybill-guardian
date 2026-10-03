package tools

import (
	"context"
	_ "embed"
	"errors"
	"fmt"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/idempotency"
	"github.com/Duang777/waybill-guardian/internal/platform"
)

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

type CarrierEvidence struct {
	ID             domain.CarrierID `json:"carrier_id"`
	Name           string           `json:"name"`
	ETAHours       int              `json:"eta_hours"`
	ReliabilityPct float64          `json:"reliability_pct"`
}

type GetWaybillOutput struct {
	WaybillID         domain.WaybillID  `json:"waybill_id"`
	Origin            string            `json:"origin"`
	Destination       string            `json:"destination"`
	Cargo             string            `json:"cargo"`
	CarrierID         domain.CarrierID  `json:"carrier_id"`
	DriverID          domain.DriverID   `json:"driver_id"`
	Status            string            `json:"status"`
	SLAHours          int               `json:"sla_hours"`
	CandidateCarriers []CarrierEvidence `json:"candidate_carriers"`
}

type GetTrackingInput struct {
	WaybillID string `json:"waybill_id" jsonschema_description:"Waybill identifier"`
}

type TrackingEvidence struct {
	Label      string  `json:"label"`
	RecordedAt string  `json:"recorded_at"`
	SpeedKPH   int     `json:"speed_kph"`
	StopHours  float64 `json:"stop_hours,omitempty"`
	Anomaly    bool    `json:"anomaly"`
}

type GetTrackingOutput struct {
	Points []TrackingEvidence `json:"points"`
}

type GetDriverInput struct {
	DriverID string `json:"driver_id" jsonschema_description:"Driver identifier"`
}

type GetDriverOutput struct {
	DriverID           domain.DriverID `json:"driver_id"`
	ContinuousDriveHrs float64         `json:"continuous_drive_hours"`
	FatigueAlert       bool            `json:"fatigue_alert"`
}

type GetRoadWeatherInput struct {
	Route string `json:"route" jsonschema_description:"Route in origin-destination form"`
}

type GetRoadWeatherOutput struct {
	Segments []RoadWeatherEvidence `json:"segments"`
}

type RoadWeatherEvidence struct {
	Segment    string `json:"segment"`
	Condition  string `json:"condition"`
	AlertLevel string `json:"alert_level"`
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

type NotificationRecipient string

const (
	RecipientShipper NotificationRecipient = "shipper"
	RecipientDriver  NotificationRecipient = "driver"
)

type SendSMSInput struct {
	WaybillID string                `json:"waybill_id" jsonschema_description:"Waybill identifier"`
	Recipient NotificationRecipient `json:"recipient" jsonschema_description:"Business recipient role: shipper or driver"`
	CarrierID string                `json:"carrier_id" jsonschema_description:"Assigned carrier identifier"`
}

type SendSMSOutput = platform.SMSReceipt

type Handlers struct {
	clients platform.Clients
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
	waybill, err := h.clients.TMS.GetWaybill(ctx, platform.GetWaybillRequest{
		WaybillID: domain.WaybillID(in.WaybillID),
	})
	if err != nil {
		return GetWaybillOutput{}, err
	}
	carriers := make([]CarrierEvidence, 0, len(waybill.CandidateCarriers))
	for _, carrier := range waybill.CandidateCarriers {
		carriers = append(carriers, CarrierEvidence{
			ID:             carrier.ID,
			Name:           carrier.Name,
			ETAHours:       carrier.ETAHours,
			ReliabilityPct: carrier.ReliabilityPct,
		})
	}
	return GetWaybillOutput{
		WaybillID:         waybill.ID,
		Origin:            waybill.Origin,
		Destination:       waybill.Destination,
		Cargo:             waybill.Cargo,
		CarrierID:         waybill.CarrierID,
		DriverID:          waybill.DriverID,
		Status:            waybill.Status,
		SLAHours:          waybill.SLAHours,
		CandidateCarriers: carriers,
	}, nil
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
	evidence := make([]TrackingEvidence, 0, len(points))
	for _, point := range points {
		evidence = append(evidence, TrackingEvidence{
			Label:      point.Label,
			RecordedAt: point.RecordedAt,
			SpeedKPH:   point.SpeedKPH,
			StopHours:  point.StopHours,
			Anomaly:    point.Anomaly,
		})
	}
	return GetTrackingOutput{Points: evidence}, nil
}

func (h *Handlers) GetDriver(ctx context.Context, in GetDriverInput) (GetDriverOutput, error) {
	if in.DriverID == "" {
		return GetDriverOutput{}, fmt.Errorf("driver_id is required")
	}
	driver, err := h.clients.TMS.GetDriver(ctx, platform.GetDriverRequest{
		DriverID: domain.DriverID(in.DriverID),
	})
	if err != nil {
		return GetDriverOutput{}, err
	}
	return GetDriverOutput{
		DriverID:           driver.ID,
		ContinuousDriveHrs: driver.ContinuousDriveHrs,
		FatigueAlert:       driver.FatigueAlert,
	}, nil
}

func (h *Handlers) GetRoadWeather(ctx context.Context, in GetRoadWeatherInput) (GetRoadWeatherOutput, error) {
	if in.Route == "" {
		return GetRoadWeatherOutput{}, fmt.Errorf("route is required")
	}
	segments, err := h.clients.Weather.GetRoadWeather(ctx, platform.GetRoadWeatherRequest{Route: in.Route})
	if err != nil {
		return GetRoadWeatherOutput{}, err
	}
	evidence := make([]RoadWeatherEvidence, 0, len(segments))
	for _, segment := range segments {
		evidence = append(evidence, RoadWeatherEvidence{
			Segment:    segment.Segment,
			Condition:  segment.Condition,
			AlertLevel: segment.AlertLevel,
		})
	}
	return GetRoadWeatherOutput{Segments: evidence}, nil
}

func (h *Handlers) Reassign(ctx context.Context, in ReassignInput) (ReassignOutput, error) {
	if err := validateReassign(in); err != nil {
		return ReassignOutput{}, err
	}
	key, err := executionKey(ctx, domain.ActionReassign)
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
	if err := validateCreateClaim(in); err != nil {
		return CreateClaimOutput{}, err
	}
	key, err := executionKey(ctx, domain.ActionCreateClaim)
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
	if err := validateSendSMS(in); err != nil {
		return SendSMSOutput{}, err
	}
	key, err := executionKey(ctx, domain.ActionSendSMS)
	if err != nil {
		return SendSMSOutput{}, err
	}
	waybill, err := h.clients.TMS.GetWaybill(ctx, platform.GetWaybillRequest{
		WaybillID: domain.WaybillID(in.WaybillID),
	})
	if err != nil {
		return SendSMSOutput{}, err
	}
	phone := waybill.ShipperPhone
	templateID := "waybill_reassigned"
	if in.Recipient == RecipientDriver {
		driver, driverErr := h.clients.TMS.GetDriver(ctx, platform.GetDriverRequest{
			DriverID: waybill.DriverID,
		})
		if driverErr != nil {
			return SendSMSOutput{}, driverErr
		}
		phone = driver.Phone
		templateID = "waybill_reassigned_driver"
	}
	return h.clients.Notification.SendSMS(ctx, platform.SendSMSRequest{
		Phone:      phone,
		TemplateID: templateID,
		Params: map[string]string{
			"waybill_id": in.WaybillID,
			"carrier_id": in.CarrierID,
		},
		IdempotencyKey: key,
	})
}

func validateReassign(in ReassignInput) error {
	if err := validateWaybillID(in.WaybillID); err != nil {
		return err
	}
	if in.CarrierID == "" {
		return fmt.Errorf("carrier_id is required")
	}
	return nil
}

func validateCreateClaim(in CreateClaimInput) error {
	if err := validateWaybillID(in.WaybillID); err != nil {
		return err
	}
	if in.ClaimType == "" {
		return fmt.Errorf("claim_type is required")
	}
	return nil
}

func validateSendSMS(in SendSMSInput) error {
	if err := validateWaybillID(in.WaybillID); err != nil {
		return err
	}
	if in.Recipient != RecipientShipper && in.Recipient != RecipientDriver {
		return fmt.Errorf("recipient must be %q or %q", RecipientShipper, RecipientDriver)
	}
	if in.CarrierID == "" {
		return fmt.Errorf("carrier_id is required")
	}
	return nil
}

func executionKey(ctx context.Context, action domain.Action) (domain.IdempotencyKey, error) {
	identity, err := idempotency.ExecutionFromContext(ctx)
	if err != nil {
		return "", err
	}
	if identity.Action != action {
		return "", errors.Join(
			idempotency.ErrInvalidIdentity,
			fmt.Errorf("execution action %q does not match tool action %q", identity.Action, action),
		)
	}
	return identity.Key, nil
}

func validateWaybillID(id string) error {
	return domain.ValidateWaybillID(domain.WaybillID(id))
}
