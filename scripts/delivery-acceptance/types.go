package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
)

const (
	configSchemaVersion        = "delivery.acceptance.config.v1"
	reportSchemaVersion        = "delivery.acceptance.report.v1"
	commandResultSchemaVersion = "delivery.acceptance.command-result.v1"
)

type suiteConfig struct {
	SchemaVersion         string         `json:"schema_version"`
	Artifact              artifactConfig `json:"artifact"`
	Browser               browserConfig  `json:"browser"`
	CommandTimeoutSeconds int            `json:"command_timeout_seconds"`
	Probes                []probeSpec    `json:"probes"`
}

type artifactConfig struct {
	Iterations        int    `json:"iterations"`
	PayloadBytes      int    `json:"payload_bytes"`
	MaxArtifactBytes  int64  `json:"max_artifact_bytes"`
	OversizeBytes     int    `json:"oversize_bytes"`
	ConcurrentWriters int    `json:"concurrent_writers"`
	MaxPeakHeapBytes  uint64 `json:"max_peak_heap_bytes"`
	MaxRecoveryNS     int64  `json:"max_recovery_ns"`
}

type browserConfig struct {
	CargoItems     int `json:"cargo_items"`
	RemountCycles  int `json:"remount_cycles"`
	StateCount     int `json:"state_count"`
	TimeoutSeconds int `json:"timeout_seconds"`
}

type probeSpec struct {
	ID                     string                   `json:"id"`
	Label                  string                   `json:"label"`
	Kind                   string                   `json:"kind"`
	CommandEnv             string                   `json:"command_env,omitempty"`
	RequiredForPublication bool                     `json:"required_for_publication"`
	ExpectedChecks         []string                 `json:"expected_checks"`
	ExpectedMeasurements   []measurementExpectation `json:"expected_measurements,omitempty"`
}

type measurementExpectation struct {
	ID               string `json:"id"`
	TaskCount        int    `json:"task_count,omitempty"`
	VehicleCount     int    `json:"vehicle_count,omitempty"`
	DepotCount       int    `json:"depot_count,omitempty"`
	CargoPerVehicle  int    `json:"cargo_per_vehicle,omitempty"`
	QueryConcurrency int    `json:"query_concurrency,omitempty"`
	SSEConnections   int    `json:"sse_connections,omitempty"`
	MinimumSamples   int    `json:"minimum_samples"`
	RequireQuality   bool   `json:"require_quality,omitempty"`
	RequireRecovery  bool   `json:"require_recovery,omitempty"`
}

type evidenceStatus string

const (
	statusPassed  evidenceStatus = "passed"
	statusBlocked evidenceStatus = "blocked"
	statusFailed  evidenceStatus = "failed"
)

type acceptanceReport struct {
	SchemaVersion  string             `json:"schema_version"`
	GeneratedAt    time.Time          `json:"generated_at"`
	ToolSHA256     string             `json:"tool_sha256"`
	ConfigSHA256   string             `json:"config_sha256"`
	SubjectSHA256  string             `json:"subject_sha256"`
	SourceRevision string             `json:"source_revision"`
	Environment    runtimeEnvironment `json:"environment"`
	Probes         []probeEvidence    `json:"probes"`
	Gate           publicationGate    `json:"publication_gate"`
}

type runtimeEnvironment struct {
	GOOS        string `json:"goos"`
	GOARCH      string `json:"goarch"`
	GoVersion   string `json:"go_version"`
	OSVersion   string `json:"os_version"`
	CPUModel    string `json:"cpu_model"`
	MemoryBytes uint64 `json:"memory_bytes"`
}

type probeEvidence struct {
	ID                     string                `json:"id"`
	Label                  string                `json:"label"`
	Kind                   string                `json:"kind"`
	RequiredForPublication bool                  `json:"required_for_publication"`
	Status                 evidenceStatus        `json:"status"`
	Reason                 string                `json:"reason,omitempty"`
	CommandSource          string                `json:"command_source,omitempty"`
	Checks                 []checkEvidence       `json:"checks"`
	Measurements           []measurementEvidence `json:"measurements"`
	Artifacts              []artifactEvidence    `json:"artifacts"`
}

type checkEvidence struct {
	ID     string `json:"id"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

type measurementEvidence struct {
	ID               string          `json:"id"`
	SampleCount      int             `json:"sample_count"`
	TaskCount        int             `json:"task_count,omitempty"`
	VehicleCount     int             `json:"vehicle_count,omitempty"`
	DepotCount       int             `json:"depot_count,omitempty"`
	CargoPerVehicle  int             `json:"cargo_per_vehicle,omitempty"`
	QueryConcurrency int             `json:"query_concurrency,omitempty"`
	SSEConnections   int             `json:"sse_connections,omitempty"`
	DurationNS       durationSummary `json:"duration_ns"`
	PeakMemoryBytes  uint64          `json:"peak_memory_bytes"`
	PeakGoroutines   int             `json:"peak_goroutines,omitempty"`
	FeasibilityPPM   int64           `json:"feasibility_ppm,omitempty"`
	QualityGapPPM    int64           `json:"quality_gap_ppm,omitempty"`
	RecoveryNS       int64           `json:"recovery_ns,omitempty"`
}

type durationSummary struct {
	Minimum int64 `json:"minimum"`
	P50     int64 `json:"p50"`
	P95     int64 `json:"p95"`
	P99     int64 `json:"p99"`
	Maximum int64 `json:"maximum"`
}

type artifactEvidence struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type publicationGate struct {
	PublicationReady bool     `json:"publication_ready"`
	PassedProbes     []string `json:"passed_probes"`
	BlockingProbes   []string `json:"blocking_probes"`
	FailedProbes     []string `json:"failed_probes"`
}

type commandResult struct {
	SchemaVersion string                `json:"schema_version"`
	ProbeID       string                `json:"probe_id"`
	Checks        []checkEvidence       `json:"checks"`
	Measurements  []measurementEvidence `json:"measurements"`
	Artifacts     []artifactEvidence    `json:"artifacts"`
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

func (config suiteConfig) validate() error {
	if config.SchemaVersion != configSchemaVersion {
		return fmt.Errorf("schema_version must be %q", configSchemaVersion)
	}
	if config.Artifact.Iterations < 20 ||
		config.Artifact.PayloadBytes <= 0 ||
		config.Artifact.MaxArtifactBytes <= int64(config.Artifact.PayloadBytes) ||
		config.Artifact.OversizeBytes <= int(config.Artifact.MaxArtifactBytes) ||
		config.Artifact.ConcurrentWriters <= 1 ||
		config.Artifact.MaxPeakHeapBytes == 0 ||
		config.Artifact.MaxRecoveryNS <= 0 {
		return fmt.Errorf("artifact probe configuration is invalid")
	}
	if config.Browser.CargoItems != 300 ||
		config.Browser.RemountCycles < 20 ||
		config.Browser.StateCount < 20 ||
		config.Browser.TimeoutSeconds <= 0 {
		return fmt.Errorf("browser probe must cover 300 cargo items, 20 remounts, and 20 states")
	}
	if config.CommandTimeoutSeconds <= 0 {
		return fmt.Errorf("command_timeout_seconds must be positive")
	}
	if len(config.Probes) == 0 {
		return fmt.Errorf("at least one probe is required")
	}
	ids := make(map[string]struct{}, len(config.Probes))
	for _, probe := range config.Probes {
		if probe.ID == "" || probe.Label == "" || len(probe.ExpectedChecks) == 0 {
			return fmt.Errorf("probe identity, label, and expected checks are required")
		}
		if _, exists := ids[probe.ID]; exists {
			return fmt.Errorf("duplicate probe %q", probe.ID)
		}
		ids[probe.ID] = struct{}{}
		if duplicate := firstDuplicate(probe.ExpectedChecks); duplicate != "" {
			return fmt.Errorf("probe %q repeats check %q", probe.ID, duplicate)
		}
		switch probe.Kind {
		case "embedded_artifact":
			if probe.ID != "artifact-resilience" || probe.CommandEnv != "" {
				return fmt.Errorf("embedded artifact probe configuration is invalid")
			}
		case "browser":
			if probe.ID != "browser-cargo" || probe.CommandEnv != "" {
				return fmt.Errorf("browser probe configuration is invalid")
			}
		case "command":
			if probe.CommandEnv == "" {
				return fmt.Errorf("command probe %q has no command_env", probe.ID)
			}
		default:
			return fmt.Errorf("probe %q has unsupported kind %q", probe.ID, probe.Kind)
		}
		measurementIDs := make(map[string]struct{}, len(probe.ExpectedMeasurements))
		for _, expected := range probe.ExpectedMeasurements {
			if expected.ID == "" || expected.MinimumSamples <= 0 {
				return fmt.Errorf("probe %q has an invalid measurement expectation", probe.ID)
			}
			if _, exists := measurementIDs[expected.ID]; exists {
				return fmt.Errorf("probe %q repeats measurement %q", probe.ID, expected.ID)
			}
			measurementIDs[expected.ID] = struct{}{}
		}
	}
	for _, required := range []string{
		"artifact-resilience",
		"browser-cargo",
		"capacity-matrix",
		"query-sse-capacity",
		"failure-recovery",
		"authorization",
		"observability",
		"supply-chain",
	} {
		if _, exists := ids[required]; !exists {
			return fmt.Errorf("required probe %q is missing", required)
		}
	}
	return nil
}

func firstDuplicate(values []string) string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return "<empty>"
		}
		if _, exists := seen[value]; exists {
			return value
		}
		seen[value] = struct{}{}
	}
	return ""
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
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

func validateCommandResult(spec probeSpec, result commandResult) error {
	if result.SchemaVersion != commandResultSchemaVersion {
		return fmt.Errorf("schema_version must be %q", commandResultSchemaVersion)
	}
	if result.ProbeID != spec.ID {
		return fmt.Errorf("probe_id is %q, want %q", result.ProbeID, spec.ID)
	}
	checks := make(map[string]checkEvidence, len(result.Checks))
	for _, check := range result.Checks {
		if check.ID == "" || strings.TrimSpace(check.Detail) == "" {
			return fmt.Errorf("checks require an id and detail")
		}
		if _, exists := checks[check.ID]; exists {
			return fmt.Errorf("duplicate check %q", check.ID)
		}
		checks[check.ID] = check
	}
	for _, expected := range spec.ExpectedChecks {
		check, exists := checks[expected]
		if !exists {
			return fmt.Errorf("missing check %q", expected)
		}
		if !check.Passed {
			return fmt.Errorf("check %q did not pass: %s", expected, check.Detail)
		}
	}
	if len(checks) != len(spec.ExpectedChecks) {
		return fmt.Errorf("command returned undeclared checks")
	}
	measurements := make(map[string]measurementEvidence, len(result.Measurements))
	for _, measurement := range result.Measurements {
		if _, exists := measurements[measurement.ID]; exists {
			return fmt.Errorf("duplicate measurement %q", measurement.ID)
		}
		if err := validateMeasurement(measurement); err != nil {
			return fmt.Errorf("measurement %q: %w", measurement.ID, err)
		}
		measurements[measurement.ID] = measurement
	}
	for _, expected := range spec.ExpectedMeasurements {
		measurement, exists := measurements[expected.ID]
		if !exists {
			return fmt.Errorf("missing measurement %q", expected.ID)
		}
		if err := validateExpectedMeasurement(expected, measurement); err != nil {
			return fmt.Errorf("measurement %q: %w", expected.ID, err)
		}
	}
	if len(measurements) != len(spec.ExpectedMeasurements) {
		return fmt.Errorf("command returned undeclared measurements")
	}
	for _, item := range result.Artifacts {
		if item.Name == "" || len(item.SHA256) != 64 || item.Bytes <= 0 {
			return fmt.Errorf("artifact evidence is incomplete")
		}
	}
	return nil
}

func validateMeasurement(value measurementEvidence) error {
	if value.ID == "" || value.SampleCount <= 0 {
		return fmt.Errorf("id and sample_count are required")
	}
	durations := value.DurationNS
	if durations.Minimum <= 0 ||
		durations.P50 < durations.Minimum ||
		durations.P95 < durations.P50 ||
		durations.P99 < durations.P95 ||
		durations.Maximum < durations.P99 {
		return fmt.Errorf("duration percentiles are invalid")
	}
	if value.PeakMemoryBytes == 0 {
		return fmt.Errorf("peak_memory_bytes is required")
	}
	if value.FeasibilityPPM < 0 || value.FeasibilityPPM > 1_000_000 {
		return fmt.Errorf("feasibility_ppm is outside [0, 1000000]")
	}
	if value.QualityGapPPM < 0 {
		return fmt.Errorf("quality_gap_ppm cannot be negative")
	}
	return nil
}

func validateExpectedMeasurement(
	expected measurementExpectation,
	actual measurementEvidence,
) error {
	if actual.SampleCount < expected.MinimumSamples {
		return fmt.Errorf("sample_count is %d, want at least %d",
			actual.SampleCount, expected.MinimumSamples)
	}
	if actual.TaskCount != expected.TaskCount ||
		actual.VehicleCount != expected.VehicleCount ||
		actual.DepotCount != expected.DepotCount ||
		actual.CargoPerVehicle != expected.CargoPerVehicle ||
		actual.QueryConcurrency != expected.QueryConcurrency ||
		actual.SSEConnections != expected.SSEConnections {
		return fmt.Errorf("scenario dimensions do not match config")
	}
	if expected.RequireQuality {
		if actual.FeasibilityPPM != 1_000_000 {
			return fmt.Errorf("feasibility_ppm is %d, want 1000000", actual.FeasibilityPPM)
		}
	}
	if expected.RequireRecovery && actual.RecoveryNS <= 0 {
		return fmt.Errorf("recovery_ns is required")
	}
	return nil
}

func buildPublicationGate(probes []probeEvidence) publicationGate {
	gate := publicationGate{
		PublicationReady: true,
		PassedProbes:     []string{},
		BlockingProbes:   []string{},
		FailedProbes:     []string{},
	}
	for _, probe := range probes {
		switch probe.Status {
		case statusPassed:
			gate.PassedProbes = append(gate.PassedProbes, probe.ID)
		case statusBlocked:
			if probe.RequiredForPublication {
				gate.PublicationReady = false
				gate.BlockingProbes = append(gate.BlockingProbes, probe.ID)
			}
		case statusFailed:
			gate.PublicationReady = false
			gate.FailedProbes = append(gate.FailedProbes, probe.ID)
		default:
			gate.PublicationReady = false
			gate.FailedProbes = append(gate.FailedProbes, probe.ID)
		}
	}
	slices.Sort(gate.PassedProbes)
	slices.Sort(gate.BlockingProbes)
	slices.Sort(gate.FailedProbes)
	return gate
}
