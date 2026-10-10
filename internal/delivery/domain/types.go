package domain

import "time"

const (
	ProblemSchemaVersion    = "delivery.problem.v1"
	PlanSchemaVersion       = "delivery.plan.v1"
	ValidationSchemaVersion = "delivery.validation.v1"
	ArtifactSchemaVersion   = "delivery.artifact.v1"
)

type TenantID string
type ProblemID string
type OptimizationRunID string
type PlanID string
type PlanRevisionID string
type ExecutionID string
type ApprovalID string
type EffectID string
type ArtifactDigest string

type DepotID string
type LocationID string
type DockID string
type RequestID string
type TaskID string
type FulfillmentUnitID string
type CargoID string
type VehicleID string
type DriverID string
type ChargerID string
type CompartmentID string
type DoorID string
type AxleID string
type TripID string
type PolicyID string

type SkillSet []string

type TimeRange struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

func (value TimeRange) Contains(instant time.Time) bool {
	return !instant.Before(value.Start) && !instant.After(value.End)
}

func (value TimeRange) ContainsRange(other TimeRange) bool {
	return !other.Start.Before(value.Start) && !other.End.After(value.End)
}

func (value TimeRange) Overlaps(other TimeRange) bool {
	return value.Start.Before(other.End) && other.Start.Before(value.End)
}

type LocationKind string

const (
	LocationCustomer LocationKind = "customer"
	LocationDepot    LocationKind = "depot"
	LocationCharger  LocationKind = "charger"
	LocationWaypoint LocationKind = "waypoint"
)

type TaskKind string

const (
	TaskPickup      TaskKind = "pickup"
	TaskDelivery    TaskKind = "delivery"
	TaskService     TaskKind = "service"
	TaskDepotLoad   TaskKind = "depot_load"
	TaskDepotUnload TaskKind = "depot_unload"
)

type SplitMode string

const (
	SplitForbidden SplitMode = "forbidden"
	SplitByUnit    SplitMode = "by_unit"
)

type EnergyKind string

const (
	EnergyCombustion EnergyKind = "combustion"
	EnergyElectric   EnergyKind = "electric"
)

type SegmentKind string

const (
	SegmentDrive    SegmentKind = "drive"
	SegmentService  SegmentKind = "service"
	SegmentWait     SegmentKind = "wait"
	SegmentBreak    SegmentKind = "break"
	SegmentCharge   SegmentKind = "charge"
	SegmentRehandle SegmentKind = "rehandle"
)

type Orientation string

const (
	OrientationLWH Orientation = "lwh"
	OrientationLHW Orientation = "lhw"
	OrientationWLH Orientation = "wlh"
	OrientationWHL Orientation = "whl"
	OrientationHLW Orientation = "hlw"
	OrientationHWL Orientation = "hwl"
)

type Axis string

const (
	AxisX Axis = "x"
	AxisY Axis = "y"
	AxisZ Axis = "z"
)

type UnassignedReason string

const (
	UnassignedCapacity        UnassignedReason = "capacity"
	UnassignedTimeWindow      UnassignedReason = "time_window"
	UnassignedSkill           UnassignedReason = "skill"
	UnassignedEnergy          UnassignedReason = "energy"
	UnassignedLoading         UnassignedReason = "loading"
	UnassignedCommitment      UnassignedReason = "commitment"
	UnassignedNoVehicle       UnassignedReason = "no_vehicle"
	UnassignedManualExclusion UnassignedReason = "manual_exclusion"
	UnassignedSearchExhausted UnassignedReason = "search_exhausted"
)

type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)
