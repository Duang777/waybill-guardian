package domain

import "time"

const OperationalFactSchemaVersion = "delivery.operational-fact.v1"

type OperationalFactHeader struct {
	SchemaVersion string    `json:"schema_version"`
	FactID        string    `json:"fact_id"`
	OccurredAt    time.Time `json:"occurred_at"`
	Watermark     string    `json:"watermark"`
	SourceSystem  string    `json:"source_system"`
}

type OperationalFact interface {
	FactHeader() OperationalFactHeader
	isOperationalFact()
}

type NewRequestFact struct {
	Meta      OperationalFactHeader `json:"meta"`
	Request   TransportRequest      `json:"request"`
	Units     []FulfillmentUnit     `json:"units"`
	Cargo     []CargoItem           `json:"cargo"`
	SourceRef SourceRef             `json:"source_ref"`
}

func (value NewRequestFact) FactHeader() OperationalFactHeader { return value.Meta }
func (NewRequestFact) isOperationalFact()                      {}

type RequestCanceledFact struct {
	Meta      OperationalFactHeader `json:"meta"`
	RequestID RequestID             `json:"request_id"`
}

func (value RequestCanceledFact) FactHeader() OperationalFactHeader { return value.Meta }
func (RequestCanceledFact) isOperationalFact()                      {}

type TaskCompletedFact struct {
	Meta        OperationalFactHeader `json:"meta"`
	TaskID      TaskID                `json:"task_id"`
	VehicleID   VehicleID             `json:"vehicle_id"`
	DriverID    DriverID              `json:"driver_id"`
	CompletedAt time.Time             `json:"completed_at"`
}

func (value TaskCompletedFact) FactHeader() OperationalFactHeader { return value.Meta }
func (TaskCompletedFact) isOperationalFact()                      {}

type VehicleUnavailableFact struct {
	Meta            OperationalFactHeader `json:"meta"`
	VehicleID       VehicleID             `json:"vehicle_id"`
	UnavailableFrom time.Time             `json:"unavailable_from"`
}

func (value VehicleUnavailableFact) FactHeader() OperationalFactHeader { return value.Meta }
func (VehicleUnavailableFact) isOperationalFact()                      {}

type DriverUnavailableFact struct {
	Meta            OperationalFactHeader `json:"meta"`
	DriverID        DriverID              `json:"driver_id"`
	UnavailableFrom time.Time             `json:"unavailable_from"`
}

func (value DriverUnavailableFact) FactHeader() OperationalFactHeader { return value.Meta }
func (DriverUnavailableFact) isOperationalFact()                      {}

type ChargerUnavailableFact struct {
	Meta            OperationalFactHeader `json:"meta"`
	ChargerID       ChargerID             `json:"charger_id"`
	UnavailableFrom time.Time             `json:"unavailable_from"`
}

func (value ChargerUnavailableFact) FactHeader() OperationalFactHeader { return value.Meta }
func (ChargerUnavailableFact) isOperationalFact()                      {}

type TravelMatrixChangedFact struct {
	Meta   OperationalFactHeader `json:"meta"`
	Travel TravelMatrix          `json:"travel"`
	Energy EnergyMatrix          `json:"energy"`
}

func (value TravelMatrixChangedFact) FactHeader() OperationalFactHeader { return value.Meta }
func (TravelMatrixChangedFact) isOperationalFact()                      {}

type VehicleSOCObservedFact struct {
	Meta      OperationalFactHeader `json:"meta"`
	VehicleID VehicleID             `json:"vehicle_id"`
	SOCWh     int64                 `json:"soc_wh"`
}

func (value VehicleSOCObservedFact) FactHeader() OperationalFactHeader { return value.Meta }
func (VehicleSOCObservedFact) isOperationalFact()                      {}

type ETADeviationFact struct {
	Meta               OperationalFactHeader `json:"meta"`
	TaskID             TaskID                `json:"task_id"`
	ProjectedServiceAt time.Time             `json:"projected_service_at"`
}

func (value ETADeviationFact) FactHeader() OperationalFactHeader { return value.Meta }
func (ETADeviationFact) isOperationalFact()                      {}

type GuardianAssignmentFact struct {
	Meta              OperationalFactHeader `json:"meta"`
	TaskID            TaskID                `json:"task_id"`
	VehicleID         VehicleID             `json:"vehicle_id"`
	DriverID          DriverID              `json:"driver_id"`
	Sequence          uint32                `json:"sequence"`
	PromisedServiceAt time.Time             `json:"promised_service_at"`
	ToleranceSeconds  int64                 `json:"tolerance_seconds"`
}

func (value GuardianAssignmentFact) FactHeader() OperationalFactHeader { return value.Meta }
func (GuardianAssignmentFact) isOperationalFact()                      {}
