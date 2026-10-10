package domain

import "time"

const (
	OperationalFactSchemaVersion   = "delivery.operational-fact.v2"
	FactFrontierSchemaVersion      = "delivery.fact-frontier.v1"
	FreezeOverrideSchemaVersion    = "delivery.freeze-override.v1"
	FreezeOverrideUseSchemaVersion = "delivery.freeze-override-use.v1"
)

type FactID string

type FactStream struct {
	SourceSystem string `json:"source_system"`
	Name         string `json:"name"`
	Partition    string `json:"partition"`
}

type FactPosition struct {
	Stream   FactStream `json:"stream"`
	Epoch    uint64     `json:"epoch"`
	Sequence uint64     `json:"sequence"`
}

type OperationalFactHeader struct {
	SchemaVersion string       `json:"schema_version"`
	FactID        FactID       `json:"fact_id"`
	Position      FactPosition `json:"position"`
	OccurredAt    time.Time    `json:"occurred_at"`
	ObservedAt    time.Time    `json:"observed_at"`
}

type OperationalFact interface {
	FactHeader() OperationalFactHeader
	isOperationalFact()
}

type StreamOpenedFact struct {
	Meta                     OperationalFactHeader `json:"meta"`
	PreviousEpoch            uint64                `json:"previous_epoch"`
	PreviousTerminalSequence uint64                `json:"previous_terminal_sequence"`
	PreviousFrontierDigest   ArtifactDigest        `json:"previous_frontier_digest"`
}

func (value StreamOpenedFact) FactHeader() OperationalFactHeader { return value.Meta }
func (StreamOpenedFact) isOperationalFact()                      {}

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

type FactRef struct {
	FactID   FactID         `json:"fact_id"`
	Position FactPosition   `json:"position"`
	Digest   ArtifactDigest `json:"digest"`
}

type LedgerFact struct {
	Fact   OperationalFact `json:"-"`
	Digest ArtifactDigest  `json:"digest"`
}

func (value LedgerFact) Ref() FactRef {
	if value.Fact == nil {
		return FactRef{Digest: value.Digest}
	}
	header := value.Fact.FactHeader()
	return FactRef{
		FactID:   header.FactID,
		Position: header.Position,
		Digest:   value.Digest,
	}
}

type FactFrontier struct {
	SchemaVersion string         `json:"schema_version"`
	Positions     []FactPosition `json:"positions"`
	Digest        ArtifactDigest `json:"digest"`
}

type FactDisposition string

const (
	FactAppended   FactDisposition = "appended"
	FactReplayed   FactDisposition = "replayed"
	FactPendingGap FactDisposition = "pending_gap"
	FactConflict   FactDisposition = "conflict"
)

type FactGap struct {
	Stream   FactStream `json:"stream"`
	Epoch    uint64     `json:"epoch"`
	Expected uint64     `json:"expected"`
	Observed uint64     `json:"observed"`
}

type FactConflictRecord struct {
	Position       FactPosition   `json:"position"`
	ExistingFactID FactID         `json:"existing_fact_id"`
	IncomingFactID FactID         `json:"incoming_fact_id"`
	ExistingDigest ArtifactDigest `json:"existing_digest"`
	IncomingDigest ArtifactDigest `json:"incoming_digest"`
	Code           string         `json:"code"`
}

type FactLedgerState struct {
	Frontier    FactFrontier         `json:"frontier"`
	Facts       []LedgerFact         `json:"-"`
	Conflicts   []FactConflictRecord `json:"conflicts"`
	Quarantined []FactStream         `json:"quarantined_streams"`
}

type FactLedgerDecision struct {
	Fact        FactRef             `json:"fact"`
	Disposition FactDisposition     `json:"disposition"`
	Gap         *FactGap            `json:"gap,omitempty"`
	Conflict    *FactConflictRecord `json:"conflict,omitempty"`
}

type FactLedgerTransition struct {
	State     FactLedgerState      `json:"state"`
	Decisions []FactLedgerDecision `json:"decisions"`
}

type FactApplication struct {
	Fact       FactRef     `json:"fact"`
	Objects    []ObjectRef `json:"objects"`
	FieldPaths []string    `json:"field_paths"`
}

type FreezeOverrideScope struct {
	TaskID                TaskID               `json:"task_id"`
	Before                FrozenTaskCommitment `json:"before"`
	AllowVehicleChange    bool                 `json:"allow_vehicle_change"`
	AllowedVehicleIDs     []VehicleID          `json:"allowed_vehicle_ids"`
	AllowDriverChange     bool                 `json:"allow_driver_change"`
	AllowedDriverIDs      []DriverID           `json:"allowed_driver_ids"`
	MaxSequenceShift      uint32               `json:"max_sequence_shift"`
	MaxETADriftSeconds    int64                `json:"max_eta_drift_seconds"`
	AllowCargoRepack      bool                 `json:"allow_cargo_repack"`
	CargoIDs              []CargoID            `json:"cargo_ids"`
	AllowedCompartmentIDs []CompartmentID      `json:"allowed_compartment_ids"`
}

type FreezeOverrideGrant struct {
	SchemaVersion        string                `json:"schema_version"`
	ApprovalID           ApprovalID            `json:"approval_id"`
	TenantID             TenantID              `json:"tenant_id"`
	PlanID               PlanID                `json:"plan_id"`
	BaseRevisionID       PlanRevisionID        `json:"base_revision_id"`
	BaseActiveVersion    uint64                `json:"base_active_version"`
	ProblemDigest        ArtifactDigest        `json:"problem_digest"`
	PolicyDigest         ArtifactDigest        `json:"policy_digest"`
	TargetFrontierDigest ArtifactDigest        `json:"target_frontier_digest"`
	Scopes               []FreezeOverrideScope `json:"scopes"`
	RequestedBy          string                `json:"requested_by"`
	ApprovedBy           string                `json:"approved_by"`
	Reason               string                `json:"reason"`
	ApprovedAt           time.Time             `json:"approved_at"`
	ExpiresAt            time.Time             `json:"expires_at"`
	Digest               ArtifactDigest        `json:"digest"`
}

type FrozenCommitmentChange struct {
	TaskID TaskID               `json:"task_id"`
	Before FrozenTaskCommitment `json:"before"`
	After  FrozenTaskCommitment `json:"after"`
}

type CargoPlacementChange struct {
	CargoID        CargoID    `json:"cargo_id"`
	AfterStopIndex uint32     `json:"after_stop_index"`
	Before         *Placement `json:"before,omitempty"`
	After          *Placement `json:"after,omitempty"`
}

type FreezeOverrideUse struct {
	SchemaVersion string                   `json:"schema_version"`
	GrantDigest   ArtifactDigest           `json:"grant_digest"`
	TaskChanges   []FrozenCommitmentChange `json:"task_changes"`
	LoadChanges   []CargoPlacementChange   `json:"load_changes"`
	Digest        ArtifactDigest           `json:"digest"`
}

type FreezeOverrideConstraint struct {
	ApprovalID  ApprovalID            `json:"approval_id"`
	GrantDigest ArtifactDigest        `json:"grant_digest"`
	Scopes      []FreezeOverrideScope `json:"scopes"`
}
