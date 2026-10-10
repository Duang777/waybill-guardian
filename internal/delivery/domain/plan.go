package domain

import "time"

type SolverIdentity struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Build   string `json:"build"`
}

type Stop struct {
	LocationID  LocationID `json:"location_id"`
	TaskIDs     []TaskID   `json:"task_ids"`
	ArrivalAt   time.Time  `json:"arrival_at"`
	ServiceAt   time.Time  `json:"service_at"`
	DepartureAt time.Time  `json:"departure_at"`
}

type DutySegment struct {
	Kind      SegmentKind `json:"kind"`
	DriverID  DriverID    `json:"driver_id"`
	From      LocationID  `json:"from_location_id"`
	To        LocationID  `json:"to_location_id"`
	StartAt   time.Time   `json:"start_at"`
	EndAt     time.Time   `json:"end_at"`
	TaskIDs   []TaskID    `json:"task_ids"`
	ChargerID ChargerID   `json:"charger_id"`
	ChargedWh int64       `json:"charged_wh"`
}

type EnergyLeg struct {
	FromStopIndex uint32    `json:"from_stop_index"`
	ToStopIndex   uint32    `json:"to_stop_index"`
	StartSOCWh    int64     `json:"start_soc_wh"`
	ConsumedWh    int64     `json:"consumed_wh"`
	ChargedWh     int64     `json:"charged_wh"`
	EndSOCWh      int64     `json:"end_soc_wh"`
	ChargerID     ChargerID `json:"charger_id"`
}

type Placement struct {
	CargoID        CargoID       `json:"cargo_id"`
	CompartmentID  CompartmentID `json:"compartment_id"`
	PositionMM     Point3        `json:"position_mm"`
	SizeMM         Box           `json:"size_mm"`
	Orientation    Orientation   `json:"orientation"`
	LoadAtTaskID   TaskID        `json:"load_at_task_id"`
	UnloadAtTaskID TaskID        `json:"unload_at_task_id"`
	DoorID         DoorID        `json:"door_id"`
}

func (value Placement) Cuboid() Cuboid {
	return Cuboid{Origin: value.PositionMM, Size: value.SizeMM}
}

type RehandleOperation struct {
	Sequence        uint16    `json:"sequence"`
	CargoID         CargoID   `json:"cargo_id"`
	StopIndex       uint32    `json:"stop_index"`
	Before          Placement `json:"before"`
	After           Placement `json:"after"`
	DurationSeconds int64     `json:"duration_seconds"`
	CostCents       int64     `json:"cost_cents"`
}

type LoadStage struct {
	AfterStopIndex uint32              `json:"after_stop_index"`
	Placements     []Placement         `json:"placements"`
	AxleLoadsG     []int64             `json:"axle_loads_g"`
	CenterOfMassMM Point3              `json:"center_of_mass_mm"`
	Rehandles      []RehandleOperation `json:"rehandles"`
}

type Trip struct {
	ID           TripID        `json:"id"`
	StartDepotID DepotID       `json:"start_depot_id"`
	EndDepotID   DepotID       `json:"end_depot_id"`
	StartAt      time.Time     `json:"start_at"`
	EndAt        time.Time     `json:"end_at"`
	Stops        []Stop        `json:"stops"`
	Schedule     []DutySegment `json:"schedule"`
	Energy       []EnergyLeg   `json:"energy"`
	LoadStages   []LoadStage   `json:"load_stages"`
}

type VehicleDuty struct {
	VehicleID VehicleID  `json:"vehicle_id"`
	DriverIDs []DriverID `json:"driver_ids"`
	Trips     []Trip     `json:"trips"`
}

type UnassignedUnit struct {
	UnitID FulfillmentUnitID `json:"unit_id"`
	Reason UnassignedReason  `json:"reason"`
	Detail string            `json:"detail"`
}

type ObjectiveVector struct {
	UnassignedRequiredUnits uint32 `json:"unassigned_required_units"`
	HardViolationCount      uint32 `json:"hard_violation_count"`
	VehiclesUsed            uint32 `json:"vehicles_used"`
	TotalCostCents          int64  `json:"total_cost_cents"`
	TotalDistanceMeters     int64  `json:"total_distance_meters"`
	TotalWaitSeconds        int64  `json:"total_wait_seconds"`
	NegativeMinVolumePPM    int64  `json:"negative_min_volume_utilization_ppm"`
	StabilityCostCents      int64  `json:"stability_cost_cents"`
}

type PlanMetrics struct {
	AssignedUnits             uint32 `json:"assigned_units"`
	UnassignedUnits           uint32 `json:"unassigned_units"`
	VehiclesUsed              uint32 `json:"vehicles_used"`
	Trips                     uint32 `json:"trips"`
	Stops                     uint32 `json:"stops"`
	TotalDistanceMeters       int64  `json:"total_distance_meters"`
	TotalDriveSeconds         int64  `json:"total_drive_seconds"`
	TotalServiceSeconds       int64  `json:"total_service_seconds"`
	TotalWaitSeconds          int64  `json:"total_wait_seconds"`
	TotalBreakSeconds         int64  `json:"total_break_seconds"`
	TotalChargeSeconds        int64  `json:"total_charge_seconds"`
	TotalRehandleSeconds      int64  `json:"total_rehandle_seconds"`
	TotalEnergyWh             int64  `json:"total_energy_wh"`
	TotalCostCents            int64  `json:"total_cost_cents"`
	TotalRehandleCostCents    int64  `json:"total_rehandle_cost_cents"`
	StabilityCostCents        int64  `json:"stability_cost_cents"`
	OnTimeTasks               uint32 `json:"on_time_tasks"`
	LateTasks                 uint32 `json:"late_tasks"`
	OnTimeRatePPM             int64  `json:"on_time_rate_ppm"`
	MinVolumeUtilizationPPM   int64  `json:"min_volume_utilization_ppm"`
	MeanVolumeUtilizationPPM  int64  `json:"mean_volume_utilization_ppm"`
	MeanPayloadUtilizationPPM int64  `json:"mean_payload_utilization_ppm"`
	MaxPayloadUtilizationPPM  int64  `json:"max_payload_utilization_ppm"`
	Rehandles                 uint32 `json:"rehandles"`
}

type Plan struct {
	SchemaVersion    string           `json:"schema_version"`
	PlanID           PlanID           `json:"plan_id"`
	RevisionID       PlanRevisionID   `json:"revision_id"`
	ProblemDigest    ArtifactDigest   `json:"problem_digest"`
	PolicyDigest     ArtifactDigest   `json:"policy_digest"`
	CommitmentDigest ArtifactDigest   `json:"commitment_digest"`
	Solver           SolverIdentity   `json:"solver"`
	ConfigDigest     ArtifactDigest   `json:"config_digest"`
	Duties           []VehicleDuty    `json:"duties"`
	Unassigned       []UnassignedUnit `json:"unassigned"`
	Objective        ObjectiveVector  `json:"objective"`
	Metrics          PlanMetrics      `json:"metrics"`
	PlanDigest       ArtifactDigest   `json:"plan_digest"`
}
