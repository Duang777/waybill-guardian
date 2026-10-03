package platform

import (
	"context"
	"errors"

	"github.com/Duang777/waybill-guardian/internal/domain"
)

var ErrNotImplemented = errors.New("real platform adapter is not implemented")

type Waybill struct {
	ID                domain.WaybillID `json:"waybill_id"`
	Origin            string           `json:"origin"`
	Destination       string           `json:"destination"`
	Cargo             string           `json:"cargo"`
	CarrierID         domain.CarrierID `json:"carrier_id"`
	DriverID          domain.DriverID  `json:"driver_id"`
	Status            string           `json:"status"`
	SLAHours          int              `json:"sla_hours"`
	ShipperPhone      string           `json:"shipper_phone"`
	CandidateCarriers []Carrier        `json:"candidate_carriers"`
}

type Carrier struct {
	ID             domain.CarrierID `json:"carrier_id"`
	Name           string           `json:"name"`
	ETAHours       int              `json:"eta_hours"`
	ReliabilityPct float64          `json:"reliability_pct"`
}

type TrackPoint struct {
	Label      string  `json:"label"`
	RecordedAt string  `json:"recorded_at"`
	Longitude  float64 `json:"longitude"`
	Latitude   float64 `json:"latitude"`
	SpeedKPH   int     `json:"speed_kph"`
	StopHours  float64 `json:"stop_hours,omitempty"`
	Anomaly    bool    `json:"anomaly"`
}

type Driver struct {
	ID                 domain.DriverID `json:"driver_id"`
	Name               string          `json:"name"`
	Phone              string          `json:"phone"`
	Plate              string          `json:"plate"`
	ContinuousDriveHrs float64         `json:"continuous_drive_hours"`
	FatigueAlert       bool            `json:"fatigue_alert"`
}

type RoadWeather struct {
	Segment    string `json:"segment"`
	Condition  string `json:"condition"`
	AlertLevel string `json:"alert_level"`
}

type ReassignOrder struct {
	OrderID   string           `json:"order_id"`
	WaybillID domain.WaybillID `json:"waybill_id"`
	CarrierID domain.CarrierID `json:"carrier_id"`
	Status    string           `json:"status"`
}

type ClaimOrder struct {
	ClaimID   string           `json:"claim_id"`
	WaybillID domain.WaybillID `json:"waybill_id"`
	ClaimType string           `json:"claim_type"`
	Status    string           `json:"status"`
}

type SMSReceipt struct {
	MessageID string `json:"message_id"`
	Status    string `json:"status"`
}

type GetWaybillRequest struct {
	WaybillID domain.WaybillID
}

type GetTrackingRequest struct {
	WaybillID domain.WaybillID
}

type GetDriverRequest struct {
	DriverID domain.DriverID
}

type GetRoadWeatherRequest struct {
	Route string
}

type ReassignRequest struct {
	WaybillID      domain.WaybillID
	CarrierID      domain.CarrierID
	IdempotencyKey domain.IdempotencyKey
}

type CreateClaimRequest struct {
	WaybillID      domain.WaybillID
	ClaimType      string
	IdempotencyKey domain.IdempotencyKey
}

type SendSMSRequest struct {
	Phone          string
	TemplateID     string
	Params         map[string]string
	IdempotencyKey domain.IdempotencyKey
}

type TMSClient interface {
	GetWaybill(context.Context, GetWaybillRequest) (Waybill, error)
	GetTracking(context.Context, GetTrackingRequest) ([]TrackPoint, error)
	GetDriver(context.Context, GetDriverRequest) (Driver, error)
	Reassign(context.Context, ReassignRequest) (ReassignOrder, error)
	CreateClaim(context.Context, CreateClaimRequest) (ClaimOrder, error)
}

type WeatherClient interface {
	GetRoadWeather(context.Context, GetRoadWeatherRequest) ([]RoadWeather, error)
}

type NotificationClient interface {
	SendSMS(context.Context, SendSMSRequest) (SMSReceipt, error)
}

type Clients struct {
	TMS          TMSClient
	Weather      WeatherClient
	Notification NotificationClient
}

type RealAdapter struct{}

func (RealAdapter) GetWaybill(context.Context, GetWaybillRequest) (Waybill, error) {
	return Waybill{}, ErrNotImplemented
}

func (RealAdapter) GetTracking(context.Context, GetTrackingRequest) ([]TrackPoint, error) {
	return nil, ErrNotImplemented
}

func (RealAdapter) GetDriver(context.Context, GetDriverRequest) (Driver, error) {
	return Driver{}, ErrNotImplemented
}

func (RealAdapter) Reassign(context.Context, ReassignRequest) (ReassignOrder, error) {
	return ReassignOrder{}, ErrNotImplemented
}

func (RealAdapter) CreateClaim(context.Context, CreateClaimRequest) (ClaimOrder, error) {
	return ClaimOrder{}, ErrNotImplemented
}

func (RealAdapter) GetRoadWeather(context.Context, GetRoadWeatherRequest) ([]RoadWeather, error) {
	return nil, ErrNotImplemented
}

func (RealAdapter) SendSMS(context.Context, SendSMSRequest) (SMSReceipt, error) {
	return SMSReceipt{}, ErrNotImplemented
}
