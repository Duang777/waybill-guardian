package filestore

import (
	"regexp"

	"github.com/Duang777/waybill-guardian/internal/platform"
)

const (
	schemaVersionV1 = "v1"
	maxFileBytes    = 16 << 20
	maxCSVRows      = 100_000
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type Loaded struct {
	Reads  platform.ReadSet
	Source SourceDescriptor
	Stats  Stats
}

type SourceDescriptor struct {
	Format        string
	SchemaVersion string
	DatasetID     string
	Digest        string
}

func (s SourceDescriptor) String() string {
	return "file:" + s.Format + ":" + s.SchemaVersion + ":" + s.DatasetID + ":" + s.Digest
}

type Stats struct {
	Waybills  int
	Anomalies int
	Hubs      int
	Vehicles  int
	Routes    int
}

type sourceRef struct {
	location string
}

type datasetDraft struct {
	SchemaVersion     string           `json:"schema_version"`
	DatasetID         string           `json:"dataset_id"`
	Hubs              []hubDraft       `json:"hubs,omitempty"`
	Vehicles          []vehicleDraft   `json:"vehicles,omitempty"`
	Routes            []routeDraft     `json:"routes,omitempty"`
	Waybills          []waybillDraft   `json:"waybills"`
	Drivers           []driverDraft    `json:"drivers"`
	WaybillCandidates []candidateDraft `json:"waybill_candidates"`
	Tracking          []trackingDraft  `json:"tracking"`
	Weather           []weatherDraft   `json:"weather"`
}

type waybillDraft struct {
	WaybillID        string       `json:"waybill_id"`
	Origin           string       `json:"origin"`
	Destination      string       `json:"destination"`
	OriginHubID      string       `json:"origin_hub_id,omitempty"`
	DestinationHubID string       `json:"destination_hub_id,omitempty"`
	RouteID          string       `json:"route_id,omitempty"`
	VehicleID        string       `json:"vehicle_id,omitempty"`
	Cargo            string       `json:"cargo"`
	CurrentCarrierID string       `json:"current_carrier_id"`
	DriverID         string       `json:"driver_id"`
	Status           string       `json:"status"`
	SLAHours         int          `json:"sla_hours"`
	ShipperPhone     string       `json:"shipper_phone"`
	Impact           *impactDraft `json:"impact,omitempty"`
	sourceRef
}

type impactDraft struct {
	NoActionETAHours    float64 `json:"no_action_eta_hours"`
	PostActionETAHours  float64 `json:"post_action_eta_hours"`
	AvoidedPenaltyCents int64   `json:"avoided_penalty_cents"`
	ReassignDeltaCents  int64   `json:"reassign_delta_cents"`
	HandlingCostCents   int64   `json:"handling_cost_cents"`
}

type hubDraft struct {
	HubID         string  `json:"hub_id"`
	Name          string  `json:"name"`
	Province      string  `json:"province"`
	City          string  `json:"city"`
	Longitude     float64 `json:"longitude"`
	Latitude      float64 `json:"latitude"`
	DailyCapacity int     `json:"daily_capacity"`
	sourceRef
}

type vehicleDraft struct {
	VehicleID        string  `json:"vehicle_id"`
	MaskedPlate      string  `json:"masked_plate"`
	Type             string  `json:"type"`
	LoadCapacityTons float64 `json:"load_capacity_tons"`
	sourceRef
}

type routeDraft struct {
	RouteID          string `json:"route_id"`
	OriginHubID      string `json:"origin_hub_id"`
	DestinationHubID string `json:"destination_hub_id"`
	DistanceKM       int    `json:"distance_km"`
	StandardHours    int    `json:"standard_hours"`
	sourceRef
}

type driverDraft struct {
	DriverID             string  `json:"driver_id"`
	Name                 string  `json:"name"`
	Phone                string  `json:"phone"`
	Plate                string  `json:"plate"`
	ContinuousDriveHours float64 `json:"continuous_drive_hours"`
	FatigueAlert         bool    `json:"fatigue_alert"`
	sourceRef
}

type candidateDraft struct {
	WaybillID      string  `json:"waybill_id"`
	Priority       int     `json:"priority"`
	CarrierID      string  `json:"carrier_id"`
	Name           string  `json:"name"`
	ETAHours       int     `json:"eta_hours"`
	ReliabilityPct float64 `json:"reliability_pct"`
	sourceRef
}

type trackingDraft struct {
	WaybillID   string   `json:"waybill_id"`
	Sequence    int      `json:"sequence"`
	Label       string   `json:"label"`
	RecordedAt  string   `json:"recorded_at"`
	Longitude   float64  `json:"longitude"`
	Latitude    float64  `json:"latitude"`
	SpeedKPH    int      `json:"speed_kph"`
	StopHours   *float64 `json:"stop_hours,omitempty"`
	Anomaly     bool     `json:"anomaly"`
	AnomalyType string   `json:"anomaly_type,omitempty"`
	sourceRef
}

type weatherDraft struct {
	RouteID     string `json:"route_id,omitempty"`
	Origin      string `json:"origin"`
	Destination string `json:"destination"`
	Sequence    int    `json:"sequence"`
	Segment     string `json:"segment"`
	Condition   string `json:"condition"`
	AlertLevel  string `json:"alert_level"`
	sourceRef
}

type routeKey struct {
	origin      string
	destination string
}
