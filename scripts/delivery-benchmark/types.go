package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

const (
	configSchemaVersion   = "delivery.benchmark.config.v1"
	manifestSchemaVersion = "delivery.benchmark.manifest.v1"
	reportSchemaVersion   = "delivery.benchmark.report.v1"
)

type suiteConfig struct {
	SchemaVersion          string                   `json:"schema_version"`
	Generator              generatorConfig          `json:"generator"`
	ReplayCount            int                      `json:"replay_count"`
	SolverTimeoutSeconds   int                      `json:"solver_timeout_seconds"`
	SolveBudgetEvaluations uint64                   `json:"solve_budget_evaluations"`
	Validator              domain.ValidatorIdentity `json:"validator"`
	Datasets               []datasetSpec            `json:"datasets"`
	Adapters               []adapterSpec            `json:"adapters"`
}

type generatorConfig struct {
	Version          string            `json:"version"`
	BaseTime         string            `json:"base_time"`
	CoordinateOrigin string            `json:"coordinate_origin"`
	DistanceRule     string            `json:"distance_rule"`
	TravelTimeRule   string            `json:"travel_time_rule"`
	Units            map[string]string `json:"units"`
	Rounding         map[string]string `json:"rounding"`
	FieldSources     map[string]string `json:"field_sources"`
}

type datasetSpec struct {
	ID           string `json:"id"`
	Seed         uint64 `json:"seed"`
	RequestCount int    `json:"request_count"`
}

type adapterSpec struct {
	ID                     string `json:"id"`
	Label                  string `json:"label"`
	Kind                   string `json:"kind"`
	CommandEnv             string `json:"command_env,omitempty"`
	CapabilityProfile      string `json:"capability_profile"`
	RequiredForPublication bool   `json:"required_for_publication"`
}

type datasetManifest struct {
	SchemaVersion string                  `json:"schema_version"`
	Generator     generatorConfig         `json:"generator"`
	Datasets      []datasetManifestRecord `json:"datasets"`
}

type datasetManifestRecord struct {
	ID                     string                `json:"id"`
	Seed                   uint64                `json:"seed"`
	RequestCount           int                   `json:"request_count"`
	TaskCount              int                   `json:"task_count"`
	CargoCount             int                   `json:"cargo_count"`
	VehicleCount           int                   `json:"vehicle_count"`
	DriverCount            int                   `json:"driver_count"`
	LocationCount          int                   `json:"location_count"`
	CanonicalBytes         int                   `json:"canonical_bytes"`
	CanonicalSHA256        string                `json:"canonical_sha256"`
	ProblemDigest          domain.ArtifactDigest `json:"problem_digest"`
	PolicyDigest           domain.ArtifactDigest `json:"policy_digest"`
	CommitmentDigest       domain.ArtifactDigest `json:"commitment_digest"`
	ReferencePlanDigest    domain.ArtifactDigest `json:"reference_plan_digest"`
	ReferenceReportDigest  domain.ArtifactDigest `json:"reference_report_digest"`
	ReferenceHardViolation int                   `json:"reference_hard_violations"`
}

type benchmarkReport struct {
	SchemaVersion       string                  `json:"schema_version"`
	GeneratedAt         time.Time               `json:"generated_at"`
	ToolSHA256          string                  `json:"tool_sha256"`
	ConfigSHA256        string                  `json:"config_sha256"`
	ManifestSHA256      string                  `json:"manifest_sha256"`
	SourceRevision      string                  `json:"source_revision"`
	ReplayCount         int                     `json:"replay_count"`
	SolveBudget         uint64                  `json:"solve_budget_evaluations"`
	SolverTimeoutSecond int                     `json:"solver_timeout_seconds"`
	Environment         runtimeEnvironment      `json:"environment"`
	Datasets            []datasetManifestRecord `json:"datasets"`
	Adapters            []adapterEvidence       `json:"adapters"`
	Gate                publicationGate         `json:"publication_gate"`
}

type runtimeEnvironment struct {
	GOOS        string `json:"goos"`
	GOARCH      string `json:"goarch"`
	GoVersion   string `json:"go_version"`
	OSVersion   string `json:"os_version"`
	CPUModel    string `json:"cpu_model"`
	MemoryBytes uint64 `json:"memory_bytes"`
}

type evidenceStatus string

const (
	statusPassed  evidenceStatus = "passed"
	statusBlocked evidenceStatus = "blocked"
	statusFailed  evidenceStatus = "failed"
)

type adapterEvidence struct {
	ID                     string            `json:"id"`
	Label                  string            `json:"label"`
	Kind                   string            `json:"kind"`
	CapabilityProfile      string            `json:"capability_profile"`
	RequiredForPublication bool              `json:"required_for_publication"`
	Status                 evidenceStatus    `json:"status"`
	Reason                 string            `json:"reason,omitempty"`
	CommandSource          string            `json:"command_source,omitempty"`
	Datasets               []datasetEvidence `json:"datasets"`
}

type datasetEvidence struct {
	DatasetID          string                `json:"dataset_id"`
	ProblemDigest      domain.ArtifactDigest `json:"problem_digest"`
	Status             evidenceStatus        `json:"status"`
	Reason             string                `json:"reason,omitempty"`
	AllValid           bool                  `json:"all_valid"`
	DigestStable       bool                  `json:"digest_stable"`
	PlanDigest         domain.ArtifactDigest `json:"plan_digest,omitempty"`
	ValidationDigest   domain.ArtifactDigest `json:"validation_digest,omitempty"`
	HardViolationCount int                   `json:"hard_violation_count"`
	SolveDuration      durationSummary       `json:"solve_duration_ns"`
	ValidationDuration durationSummary       `json:"validation_duration_ns"`
	Metrics            domain.PlanMetrics    `json:"metrics"`
	Runs               []replayEvidence      `json:"runs"`
}

type durationSummary struct {
	Minimum int64 `json:"minimum"`
	Median  int64 `json:"median"`
	P95     int64 `json:"p95"`
	Maximum int64 `json:"maximum"`
}

type replayEvidence struct {
	Iteration            int                   `json:"iteration"`
	SolveDurationNS      int64                 `json:"solve_duration_ns"`
	ValidationDurationNS int64                 `json:"validation_duration_ns"`
	PlanDigest           domain.ArtifactDigest `json:"plan_digest,omitempty"`
	ValidationDigest     domain.ArtifactDigest `json:"validation_digest,omitempty"`
	Valid                bool                  `json:"valid"`
	HardViolationCount   int                   `json:"hard_violation_count"`
	AssignedUnits        uint32                `json:"assigned_units"`
	UnassignedUnits      uint32                `json:"unassigned_units"`
	TotalDistanceMeters  int64                 `json:"total_distance_meters"`
	TotalCostCents       int64                 `json:"total_cost_cents"`
}

type publicationGate struct {
	ReferenceEvidencePassed bool     `json:"reference_evidence_passed"`
	PublicationReady        bool     `json:"publication_ready"`
	BlockingAdapters        []string `json:"blocking_adapters"`
	FailedAdapters          []string `json:"failed_adapters"`
}

func loadConfig(path string) (suiteConfig, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return suiteConfig{}, nil, fmt.Errorf("read config: %w", err)
	}
	var config suiteConfig
	if err := decodeStrict(raw, &config); err != nil {
		return suiteConfig{}, nil, fmt.Errorf("decode config: %w", err)
	}
	if err := config.validate(); err != nil {
		return suiteConfig{}, nil, err
	}
	return config, raw, nil
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	err := decoder.Decode(&trailing)
	if err == nil {
		return fmt.Errorf("trailing JSON value")
	}
	if !errors.Is(err, io.EOF) {
		return fmt.Errorf("invalid trailing JSON: %w", err)
	}
	return nil
}

func (config suiteConfig) validate() error {
	if config.SchemaVersion != configSchemaVersion {
		return fmt.Errorf("schema_version must be %q", configSchemaVersion)
	}
	if config.Generator.Version == "" {
		return fmt.Errorf("generator version is required")
	}
	if _, err := time.Parse(time.RFC3339, config.Generator.BaseTime); err != nil {
		return fmt.Errorf("generator base_time: %w", err)
	}
	if config.ReplayCount < 20 {
		return fmt.Errorf("replay_count must be at least 20")
	}
	if config.SolverTimeoutSeconds <= 0 || config.SolveBudgetEvaluations == 0 {
		return fmt.Errorf("solver timeout and evaluation budget must be positive")
	}
	if config.Validator.Name == "" || config.Validator.Version == "" ||
		config.Validator.Build == "" {
		return fmt.Errorf("validator identity is incomplete")
	}
	if len(config.Datasets) == 0 || len(config.Adapters) == 0 {
		return fmt.Errorf("at least one dataset and adapter are required")
	}
	datasetIDs := make(map[string]struct{}, len(config.Datasets))
	for _, dataset := range config.Datasets {
		if dataset.ID == "" || dataset.Seed == 0 || dataset.RequestCount <= 0 {
			return fmt.Errorf("dataset identity, seed, and request_count are required")
		}
		if _, exists := datasetIDs[dataset.ID]; exists {
			return fmt.Errorf("duplicate dataset %q", dataset.ID)
		}
		datasetIDs[dataset.ID] = struct{}{}
	}
	adapterIDs := make(map[string]struct{}, len(config.Adapters))
	for _, adapter := range config.Adapters {
		if adapter.ID == "" || adapter.Label == "" || adapter.CapabilityProfile == "" {
			return fmt.Errorf("adapter identity, label, and capability_profile are required")
		}
		if _, exists := adapterIDs[adapter.ID]; exists {
			return fmt.Errorf("duplicate adapter %q", adapter.ID)
		}
		adapterIDs[adapter.ID] = struct{}{}
		switch adapter.Kind {
		case "embedded_reference":
			if adapter.ID != "reference-baseline" || adapter.CommandEnv != "" {
				return fmt.Errorf("embedded reference adapter has invalid configuration")
			}
		case "command":
			if adapter.CommandEnv == "" {
				return fmt.Errorf("command adapter %q has no command_env", adapter.ID)
			}
		default:
			return fmt.Errorf("adapter %q has unsupported kind %q", adapter.ID, adapter.Kind)
		}
	}
	referenceIndex := slices.IndexFunc(config.Adapters, func(value adapterSpec) bool {
		return value.ID == "reference-baseline"
	})
	if referenceIndex < 0 {
		return fmt.Errorf("reference-baseline adapter is required")
	}
	return nil
}
