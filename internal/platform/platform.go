package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
)

var (
	ErrNotFound       = errors.New("platform resource not found")
	ErrNotImplemented = errors.New("real platform adapter is not implemented")
)

type EffectDisposition string

const (
	EffectSucceeded       EffectDisposition = "succeeded"
	EffectRetryableFailed EffectDisposition = "retryable_failed"
	EffectPermanentFailed EffectDisposition = "permanent_failed"
	EffectUnknown         EffectDisposition = "unknown"
)

type EffectResult struct {
	Disposition EffectDisposition `json:"disposition"`
	ExternalRef string            `json:"external_ref,omitempty"`
	Response    json.RawMessage   `json:"response,omitempty"`
	RetryAfter  time.Duration     `json:"retry_after,omitempty"`
}

type EffectError struct {
	Disposition EffectDisposition
	Err         error
}

func (e *EffectError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("platform effect failed with disposition %q", e.Disposition)
}

func (e *EffectError) Unwrap() error {
	return e.Err
}

func RetryableEffectError(err error) error {
	return &EffectError{Disposition: EffectRetryableFailed, Err: err}
}

func PermanentEffectError(err error) error {
	return &EffectError{Disposition: EffectPermanentFailed, Err: err}
}

func UnknownEffectError(err error) error {
	return &EffectError{Disposition: EffectUnknown, Err: err}
}

func EffectDispositionOf(err error) EffectDisposition {
	if err == nil {
		return EffectSucceeded
	}
	var effectErr *EffectError
	if errors.As(err, &effectErr) {
		switch effectErr.Disposition {
		case EffectRetryableFailed, EffectPermanentFailed, EffectUnknown:
			return effectErr.Disposition
		}
	}
	return EffectUnknown
}

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

type WaybillSummary struct {
	WaybillID      domain.WaybillID
	Origin         string
	Destination    string
	Status         string
	HasAnomaly     bool
	AnomalyLabel   string
	LastRecordedAt time.Time
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

type LookupEffectRequest struct {
	Action         domain.Action
	IdempotencyKey domain.IdempotencyKey
}

type TMSReader interface {
	GetWaybill(context.Context, GetWaybillRequest) (Waybill, error)
	GetTracking(context.Context, GetTrackingRequest) ([]TrackPoint, error)
	GetDriver(context.Context, GetDriverRequest) (Driver, error)
}

type WeatherReader interface {
	GetRoadWeather(context.Context, GetRoadWeatherRequest) ([]RoadWeather, error)
}

type WaybillCatalog interface {
	ListWaybills(context.Context) ([]WaybillSummary, error)
}

type ReadSet struct {
	TMS     TMSReader
	Weather WeatherReader
	Catalog WaybillCatalog
}

type TMSClient interface {
	TMSReader
	Reassign(context.Context, ReassignRequest) (ReassignOrder, error)
	CreateClaim(context.Context, CreateClaimRequest) (ClaimOrder, error)
	LookupEffect(context.Context, LookupEffectRequest) (EffectResult, error)
}

type WeatherClient interface {
	WeatherReader
}

type NotificationClient interface {
	SendSMS(context.Context, SendSMSRequest) (SMSReceipt, error)
	LookupEffect(context.Context, LookupEffectRequest) (EffectResult, error)
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

func (RealAdapter) LookupEffect(context.Context, LookupEffectRequest) (EffectResult, error) {
	return EffectResult{Disposition: EffectUnknown}, ErrNotImplemented
}
