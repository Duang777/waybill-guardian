package platform

import (
	"context"
	"errors"
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

type Waybill struct {
	ID                domain.WaybillID
	Origin            string
	Destination       string
	Cargo             string
	CarrierID         domain.CarrierID
	DriverID          domain.DriverID
	Status            string
	SLAHours          int
	ShipperPhone      string
	CandidateCarriers []Carrier
}

type Carrier struct {
	ID             domain.CarrierID
	Name           string
	ETAHours       int
	ReliabilityPct float64
}

type TrackPoint struct {
	Label      string
	RecordedAt string
	Longitude  float64
	Latitude   float64
	SpeedKPH   int
	StopHours  float64
	Anomaly    bool
}

type Driver struct {
	ID                 domain.DriverID
	Name               string
	Phone              string
	Plate              string
	ContinuousDriveHrs float64
	FatigueAlert       bool
}

type RoadWeather struct {
	Segment    string
	Condition  string
	AlertLevel string
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
