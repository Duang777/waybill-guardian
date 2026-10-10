package domain

import "time"

type Location struct {
	ID                LocationID   `json:"id"`
	Name              string       `json:"name"`
	Kind              LocationKind `json:"kind"`
	LatitudeMicroDeg  int64        `json:"latitude_microdegrees"`
	LongitudeMicroDeg int64        `json:"longitude_microdegrees"`
}

type Dock struct {
	ID           DockID      `json:"id"`
	Availability []TimeRange `json:"availability"`
}

type Depot struct {
	ID             DepotID    `json:"id"`
	LocationID     LocationID `json:"location_id"`
	Docks          []Dock     `json:"docks"`
	AllowTripStart bool       `json:"allow_trip_start"`
	AllowTripEnd   bool       `json:"allow_trip_end"`
}

type SoftTimeWindow struct {
	Window                  TimeRange `json:"window"`
	EarlyPenaltyCentsPerSec int64     `json:"early_penalty_cents_per_second"`
	LatePenaltyCentsPerSec  int64     `json:"late_penalty_cents_per_second"`
}

type ServiceTask struct {
	ID             TaskID              `json:"id"`
	Kind           TaskKind            `json:"kind"`
	LocationID     LocationID          `json:"location_id"`
	HardWindows    []TimeRange         `json:"hard_windows"`
	SoftWindows    []SoftTimeWindow    `json:"soft_windows"`
	ServiceSeconds int64               `json:"service_seconds"`
	PredecessorIDs []TaskID            `json:"predecessor_ids"`
	UnitIDs        []FulfillmentUnitID `json:"unit_ids"`
	RequiredSkills SkillSet            `json:"required_skills"`
	MaxRideSeconds int64               `json:"max_ride_seconds"`
}

type SplitPolicy struct {
	Mode             SplitMode `json:"mode"`
	MinUnitsPerSplit uint16    `json:"min_units_per_split"`
	MaxSplits        uint16    `json:"max_splits"`
	SameVehicle      bool      `json:"same_vehicle"`
	SameTrip         bool      `json:"same_trip"`
}

type TransportRequest struct {
	ID             RequestID           `json:"id"`
	Priority       int32               `json:"priority"`
	Required       bool                `json:"required"`
	Tasks          []ServiceTask       `json:"tasks"`
	UnitIDs        []FulfillmentUnitID `json:"unit_ids"`
	Split          SplitPolicy         `json:"split"`
	RequiredSkills SkillSet            `json:"required_skills"`
}

type FulfillmentUnit struct {
	ID            FulfillmentUnitID `json:"id"`
	RequestID     RequestID         `json:"request_id"`
	AtomicGroupID string            `json:"atomic_group_id"`
	CargoIDs      []CargoID         `json:"cargo_ids"`
	Quantity      int64             `json:"quantity"`
}

type CargoItem struct {
	ID                  CargoID           `json:"id"`
	UnitID              FulfillmentUnitID `json:"unit_id"`
	SizeMM              Box               `json:"size_mm"`
	WeightG             int64             `json:"weight_g"`
	AllowedOrientations []Orientation     `json:"allowed_orientations"`
	FragileTopOnly      bool              `json:"fragile_top_only"`
	MaxTopLoadG         int64             `json:"max_top_load_g"`
	MinSupportPPM       int64             `json:"min_support_ppm"`
	TemperatureZone     string            `json:"temperature_zone"`
	CargoClass          string            `json:"cargo_class"`
	IncompatibleClasses []string          `json:"incompatible_classes"`
}

type Compartment struct {
	ID                  CompartmentID `json:"id"`
	Bounds              Cuboid        `json:"bounds"`
	MaxPayloadG         int64         `json:"max_payload_g"`
	TemperatureZones    []string      `json:"temperature_zones"`
	AllowedCargoClasses []string      `json:"allowed_cargo_classes"`
	Obstacles           []Cuboid      `json:"obstacles"`
}

type Door struct {
	ID             DoorID        `json:"id"`
	CompartmentID  CompartmentID `json:"compartment_id"`
	Opening        Cuboid        `json:"opening"`
	ExtractionAxis Axis          `json:"extraction_axis"`
	Direction      int8          `json:"direction"`
}

type Axle struct {
	ID          AxleID `json:"id"`
	PositionXMM int64  `json:"position_x_mm"`
	MaxLoadG    int64  `json:"max_load_g"`
}

type CGEnvelope struct {
	Min Point3 `json:"min"`
	Max Point3 `json:"max"`
}

type ChargingBand struct {
	FromSOCPPM int64 `json:"from_soc_ppm"`
	ToSOCPPM   int64 `json:"to_soc_ppm"`
	PowerW     int64 `json:"power_w"`
}

type EnergySpec struct {
	Kind                EnergyKind     `json:"kind"`
	MatrixProfileID     string         `json:"matrix_profile_id"`
	BatteryCapacityWh   int64          `json:"battery_capacity_wh"`
	InitialSOCWh        int64          `json:"initial_soc_wh"`
	ReserveSOCWh        int64          `json:"reserve_soc_wh"`
	ConnectorTypes      []string       `json:"connector_types"`
	ConsumptionWhPerKM  int64          `json:"consumption_wh_per_km"`
	LoadWhPerKMPerTonne int64          `json:"load_wh_per_km_per_tonne"`
	ChargingCurve       []ChargingBand `json:"charging_curve"`
}

type Vehicle struct {
	ID               VehicleID     `json:"id"`
	HomeDepotID      DepotID       `json:"home_depot_id"`
	Availability     []TimeRange   `json:"availability"`
	Skills           SkillSet      `json:"skills"`
	Compartments     []Compartment `json:"compartments"`
	Doors            []Door        `json:"doors"`
	Axles            []Axle        `json:"axles"`
	CGEnvelope       CGEnvelope    `json:"cg_envelope"`
	MaxTrips         uint16        `json:"max_trips"`
	MaxGrossWeightG  int64         `json:"max_gross_weight_g"`
	TareWeightG      int64         `json:"tare_weight_g"`
	FixedCostCents   int64         `json:"fixed_cost_cents"`
	DistanceCostCPKM int64         `json:"distance_cost_cents_per_km"`
	WorkCostCPH      int64         `json:"work_cost_cents_per_hour"`
	EnergyCostCPKWh  int64         `json:"energy_cost_cents_per_kwh"`
	Energy           EnergySpec    `json:"energy"`
}

type DriverRegulation struct {
	MaxContinuousDriveSeconds int64 `json:"max_continuous_drive_seconds"`
	RequiredBreakSeconds      int64 `json:"required_break_seconds"`
	MaxDutySeconds            int64 `json:"max_duty_seconds"`
	MaxDriveSeconds           int64 `json:"max_drive_seconds"`
	MinRestBetweenDutySeconds int64 `json:"min_rest_between_duty_seconds"`
}

type Driver struct {
	ID            DriverID         `json:"id"`
	Skills        SkillSet         `json:"skills"`
	Shift         TimeRange        `json:"shift"`
	StartLocation LocationID       `json:"start_location_id"`
	EndLocations  []LocationID     `json:"end_location_ids"`
	Regulation    DriverRegulation `json:"regulation"`
}

type ChargingStation struct {
	ID             ChargerID   `json:"id"`
	LocationID     LocationID  `json:"location_id"`
	ConnectorTypes []string    `json:"connector_types"`
	Availability   []TimeRange `json:"availability"`
	Capacity       uint16      `json:"capacity"`
	MaxPowerW      int64       `json:"max_power_w"`
}

type TravelMatrix struct {
	NodeIDs        []LocationID `json:"node_ids"`
	DistanceMeters []int64      `json:"distance_meters"`
	TravelSeconds  []int64      `json:"travel_seconds"`
}

type EnergyProfileMatrix struct {
	ProfileID      string  `json:"profile_id"`
	BaseWh         []int64 `json:"base_wh"`
	LoadWhPerTonne []int64 `json:"load_wh_per_tonne"`
}

type EnergyMatrix struct {
	NodeIDs  []LocationID          `json:"node_ids"`
	Profiles []EnergyProfileMatrix `json:"profiles"`
}

type StabilityPenalty struct {
	VehicleChangeCents  int64 `json:"vehicle_change_cents"`
	DriverChangeCents   int64 `json:"driver_change_cents"`
	SequenceChangeCents int64 `json:"sequence_change_cents"`
	ETADriftCentsPerSec int64 `json:"eta_drift_cents_per_second"`
	ReloadCents         int64 `json:"reload_cents"`
}

type PlanningPolicy struct {
	ID                        PolicyID         `json:"id"`
	Version                   uint64           `json:"version"`
	DefaultMinSupportPPM      int64            `json:"default_min_support_ppm"`
	MaxRehandlesPerStop       uint16           `json:"max_rehandles_per_stop"`
	RehandleSecondsPerCargo   int64            `json:"rehandle_seconds_per_cargo"`
	RehandleCostCentsPerCargo int64            `json:"rehandle_cost_cents_per_cargo"`
	FreezeWindowSeconds       int64            `json:"freeze_window_seconds"`
	ETAToleranceSeconds       int64            `json:"eta_tolerance_seconds"`
	RequiredOrderPenaltyCents int64            `json:"required_order_penalty_cents"`
	OptionalOrderPenaltyCents int64            `json:"optional_order_penalty_cents"`
	Stability                 StabilityPenalty `json:"stability"`
	AllowedMixedCargoClasses  [][]string       `json:"allowed_mixed_cargo_classes"`
}

type ExecutedTaskCommitment struct {
	TaskID      TaskID    `json:"task_id"`
	VehicleID   VehicleID `json:"vehicle_id"`
	DriverID    DriverID  `json:"driver_id"`
	CompletedAt time.Time `json:"completed_at"`
}

type FrozenTaskCommitment struct {
	TaskID            TaskID    `json:"task_id"`
	VehicleID         VehicleID `json:"vehicle_id"`
	DriverID          DriverID  `json:"driver_id"`
	Sequence          uint32    `json:"sequence"`
	PromisedServiceAt time.Time `json:"promised_service_at"`
	ToleranceSeconds  int64     `json:"tolerance_seconds"`
}

type InTransitCargoCommitment struct {
	CargoID       CargoID       `json:"cargo_id"`
	VehicleID     VehicleID     `json:"vehicle_id"`
	CompartmentID CompartmentID `json:"compartment_id"`
}

type SoftTaskCommitment struct {
	TaskID           TaskID    `json:"task_id"`
	VehicleID        VehicleID `json:"vehicle_id"`
	DriverID         DriverID  `json:"driver_id"`
	Sequence         uint32    `json:"sequence"`
	PlannedServiceAt time.Time `json:"planned_service_at"`
}

type SoftCargoCommitment struct {
	CargoID        CargoID       `json:"cargo_id"`
	VehicleID      VehicleID     `json:"vehicle_id"`
	AfterStopIndex uint32        `json:"after_stop_index"`
	CompartmentID  CompartmentID `json:"compartment_id"`
	DoorID         DoorID        `json:"door_id"`
	PositionMM     Point3        `json:"position_mm"`
	Orientation    Orientation   `json:"orientation"`
}

type CommitmentSet struct {
	BasePlanDigest ArtifactDigest             `json:"base_plan_digest"`
	FactWatermark  string                     `json:"fact_watermark"`
	Executed       []ExecutedTaskCommitment   `json:"executed"`
	Frozen         []FrozenTaskCommitment     `json:"frozen"`
	InTransit      []InTransitCargoCommitment `json:"in_transit"`
	Soft           []SoftTaskCommitment       `json:"soft"`
	SoftCargo      []SoftCargoCommitment      `json:"soft_cargo,omitempty"`
	FreezeOverride *FreezeOverrideConstraint  `json:"freeze_override,omitempty"`
}

type SourceRef struct {
	System       string    `json:"system"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	Version      string    `json:"version"`
	ObservedAt   time.Time `json:"observed_at"`
}

type ProblemSnapshot struct {
	SchemaVersion string    `json:"schema_version"`
	TenantID      TenantID  `json:"tenant_id"`
	ProblemID     ProblemID `json:"problem_id"`
	Version       uint64    `json:"version"`
	Horizon       TimeRange `json:"horizon"`

	Locations   []Location         `json:"locations"`
	Depots      []Depot            `json:"depots"`
	Requests    []TransportRequest `json:"requests"`
	Units       []FulfillmentUnit  `json:"units"`
	Cargo       []CargoItem        `json:"cargo"`
	Vehicles    []Vehicle          `json:"vehicles"`
	Drivers     []Driver           `json:"drivers"`
	Chargers    []ChargingStation  `json:"chargers"`
	Travel      TravelMatrix       `json:"travel"`
	Energy      EnergyMatrix       `json:"energy"`
	Policy      PlanningPolicy     `json:"policy"`
	Commitments CommitmentSet      `json:"commitments"`
	SourceRefs  []SourceRef        `json:"source_refs"`

	ProblemDigest    ArtifactDigest `json:"problem_digest"`
	PolicyDigest     ArtifactDigest `json:"policy_digest"`
	CommitmentDigest ArtifactDigest `json:"commitment_digest"`
	CreatedAt        time.Time      `json:"created_at"`
}
