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
	DigestPrefix  string
}

func (s SourceDescriptor) String() string {
	return "file:" + s.Format + ":" + s.SchemaVersion + ":" + s.DatasetID + ":" + s.DigestPrefix
}

type Stats struct {
	Waybills  int
	Anomalies int
}

type sourceRef struct {
	location string
}

type datasetDraft struct {
	SchemaVersion     string           `json:"schema_version"`
	DatasetID         string           `json:"dataset_id"`
	Waybills          []waybillDraft   `json:"waybills"`
	Drivers           []driverDraft    `json:"drivers"`
	WaybillCandidates []candidateDraft `json:"waybill_candidates"`
	Tracking          []trackingDraft  `json:"tracking"`
	Weather           []weatherDraft   `json:"weather"`
}

type waybillDraft struct {
	WaybillID        string `json:"waybill_id"`
	Origin           string `json:"origin"`
	Destination      string `json:"destination"`
	Cargo            string `json:"cargo"`
	CurrentCarrierID string `json:"current_carrier_id"`
	DriverID         string `json:"driver_id"`
	Status           string `json:"status"`
	SLAHours         int    `json:"sla_hours"`
	ShipperPhone     string `json:"shipper_phone"`
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
	WaybillID  string   `json:"waybill_id"`
	Sequence   int      `json:"sequence"`
	Label      string   `json:"label"`
	RecordedAt string   `json:"recorded_at"`
	Longitude  float64  `json:"longitude"`
	Latitude   float64  `json:"latitude"`
	SpeedKPH   int      `json:"speed_kph"`
	StopHours  *float64 `json:"stop_hours,omitempty"`
	Anomaly    bool     `json:"anomaly"`
	sourceRef
}

type weatherDraft struct {
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
