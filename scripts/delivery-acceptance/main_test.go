package main

import (
	"context"
	"encoding/json"
	"flag"
	"strings"
	"testing"
	"time"
)

func TestConfigFreezesRequiredAcceptanceMatrix(t *testing.T) {
	config, _, err := loadConfig("config.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Probes) != 8 {
		t.Fatalf("probe count = %d, want 8", len(config.Probes))
	}
	capacity := probeByID(t, config, "capacity-matrix")
	if len(capacity.ExpectedMeasurements) != 3 {
		t.Fatalf("capacity scenario count = %d, want 3", len(capacity.ExpectedMeasurements))
	}
	want := []measurementExpectation{
		{
			ID: "tasks-100", TaskCount: 100, VehicleCount: 10,
			DepotCount: 1, CargoPerVehicle: 300, MinimumSamples: 20,
			RequireQuality: true,
		},
		{
			ID: "tasks-500", TaskCount: 500, VehicleCount: 50,
			DepotCount: 5, CargoPerVehicle: 300, MinimumSamples: 20,
			RequireQuality: true,
		},
		{
			ID: "tasks-2000", TaskCount: 2000, VehicleCount: 200,
			DepotCount: 20, CargoPerVehicle: 300, MinimumSamples: 20,
			RequireQuality: true,
		},
	}
	for index, expected := range want {
		if capacity.ExpectedMeasurements[index] != expected {
			t.Fatalf(
				"capacity scenario %d = %+v, want %+v",
				index,
				capacity.ExpectedMeasurements[index],
				expected,
			)
		}
	}
	concurrency := probeByID(t, config, "query-sse-capacity")
	actual := concurrency.ExpectedMeasurements[0]
	if actual.QueryConcurrency != 100 || actual.SSEConnections != 500 {
		t.Fatalf("concurrency scenario = %+v", actual)
	}
}

func TestArtifactProbeMeasuresIsolationCorruptionAndRecovery(t *testing.T) {
	config, _, err := loadConfig("config.json")
	if err != nil {
		t.Fatal(err)
	}
	config.Artifact.PayloadBytes = 1024
	config.Artifact.MaxArtifactBytes = 4096
	config.Artifact.OversizeBytes = 8192
	spec := probeByID(t, config, "artifact-resilience")
	evidence := runArtifactProbe(context.Background(), spec, config.Artifact)
	if evidence.Status != statusPassed {
		t.Fatalf("artifact probe = %s: %s", evidence.Status, evidence.Reason)
	}
	if len(evidence.Measurements) != 4 {
		t.Fatalf("measurement count = %d, want 4", len(evidence.Measurements))
	}
	for _, check := range evidence.Checks {
		if !check.Passed {
			t.Fatalf("check %q failed: %s", check.ID, check.Detail)
		}
	}
	if evidence.Measurements[3].RecoveryNS <= 0 {
		t.Fatal("restore measurement has no recovery time")
	}
}

func TestCommandResultRequiresEveryDeclaredCheckAndMeasurement(t *testing.T) {
	config, _, err := loadConfig("config.json")
	if err != nil {
		t.Fatal(err)
	}
	spec := probeByID(t, config, "query-sse-capacity")
	result := commandResult{
		SchemaVersion: commandResultSchemaVersion,
		ProbeID:       spec.ID,
		Checks:        passingChecks(spec.ExpectedChecks),
		Measurements: []measurementEvidence{{
			ID:               "queries-100-sse-500",
			SampleCount:      20,
			QueryConcurrency: 100,
			SSEConnections:   500,
			DurationNS: durationSummary{
				Minimum: 1, P50: 2, P95: 3, P99: 4, Maximum: 5,
			},
			PeakMemoryBytes: 1024,
			FeasibilityPPM:  1_000_000,
		}},
	}
	if err := validateCommandResult(spec, result); err != nil {
		t.Fatalf("valid command result rejected: %v", err)
	}
	result.Checks[0].Passed = false
	if err := validateCommandResult(spec, result); err == nil {
		t.Fatal("failed required check was accepted")
	}
	result.Checks[0].Passed = true
	result.Measurements[0].SSEConnections = 499
	if err := validateCommandResult(spec, result); err == nil {
		t.Fatal("wrong scenario dimensions were accepted")
	}
}

func TestCommandProtocolUsesJSONArrayAndResultPlaceholder(t *testing.T) {
	if err := validateAcceptanceCommand([]string{"sh", "-c", "test"}); err == nil {
		t.Fatal("command without {result} was accepted")
	}
	command := []string{
		"./scripts/accept-capacity",
		"--config", "{config}",
		"--result", "{result}",
		"--probe", "{probe}",
	}
	if err := validateAcceptanceCommand(command); err != nil {
		t.Fatal(err)
	}
	expanded := expandCommand(command, map[string]string{
		"{config}": "/repo/config.json",
		"{result}": "/tmp/result.json",
		"{probe}":  "capacity-matrix",
	})
	if strings.Join(expanded, " ") !=
		"./scripts/accept-capacity --config /repo/config.json --result /tmp/result.json --probe capacity-matrix" {
		t.Fatalf("expanded command = %q", expanded)
	}
}

func TestStrictJSONRejectsUnknownFieldsAndTrailingValues(t *testing.T) {
	for _, raw := range []string{
		`{"value":1,"unknown":2}`,
		`{"value":1} {"value":2}`,
		`{"value":1} trailing`,
	} {
		var target struct {
			Value int `json:"value"`
		}
		if err := decodeStrict([]byte(raw), &target); err == nil {
			t.Fatalf("decodeStrict accepted %q", raw)
		}
	}
}

func TestCommandsRejectPositionalArguments(t *testing.T) {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	if err := flags.Parse([]string{"check"}); err != nil {
		t.Fatal(err)
	}
	err := rejectPositionalArguments(flags)
	if err == nil {
		t.Fatal("positional argument was accepted")
	}
	if !strings.Contains(err.Error(), "run: unexpected positional arguments: check") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBrowserResultParserUsesFinalJSONDocument(t *testing.T) {
	raw := []byte("npm output\nmore output\n" + `{
  "ok": true,
  "largeCanvas": {
    "width": 640,
    "height": 480,
    "colors": 9,
    "chromaticPixels": 4,
    "signature": 42
  },
  "stateMatrix": ["a"],
  "mountSamples": [{
    "documents": 1,
    "nodes": 2,
    "jsEventListeners": 3
  }],
  "desktop": "desktop.png",
  "mobile": "mobile.png",
  "capacity": "capacity.png"
}`)
	result, err := parseBrowserResult(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.LargeCanvas.Colors != 9 {
		t.Fatalf("browser result = %+v", result)
	}
}

func TestPublicationGateSeparatesBlockedAndFailedProbes(t *testing.T) {
	gate := buildPublicationGate([]probeEvidence{
		{ID: "passed", RequiredForPublication: true, Status: statusPassed},
		{ID: "blocked", RequiredForPublication: true, Status: statusBlocked},
		{ID: "failed", RequiredForPublication: true, Status: statusFailed},
	})
	if gate.PublicationReady {
		t.Fatal("gate passed with blocked and failed probes")
	}
	if strings.Join(gate.PassedProbes, ",") != "passed" ||
		strings.Join(gate.BlockingProbes, ",") != "blocked" ||
		strings.Join(gate.FailedProbes, ",") != "failed" {
		t.Fatalf("gate = %+v", gate)
	}
}

func TestReportVerifierRejectsSubjectDigestDrift(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	config, configRaw, err := loadConfig("config.json")
	if err != nil {
		t.Fatal(err)
	}
	report := acceptanceReport{
		SchemaVersion:  reportSchemaVersion,
		GeneratedAt:    testTime(t),
		ToolSHA256:     acceptanceToolDigest(root),
		ConfigSHA256:   digestBytes(configRaw),
		SubjectSHA256:  acceptanceSubjectDigest(root),
		SourceRevision: "test",
		Probes:         blockedProbes(config),
	}
	report.Gate = buildPublicationGate(report.Probes)
	if err := verifyReport(root, config, configRaw, report); err != nil {
		t.Fatalf("valid report rejected: %v", err)
	}
	report.SubjectSHA256 = strings.Repeat("f", 64)
	if err := verifyReport(root, config, configRaw, report); err == nil {
		t.Fatal("stale subject digest was accepted")
	}
}

func probeByID(t *testing.T, config suiteConfig, id string) probeSpec {
	t.Helper()
	for _, spec := range config.Probes {
		if spec.ID == id {
			return spec
		}
	}
	t.Fatalf("probe %q not found", id)
	return probeSpec{}
}

func passingChecks(ids []string) []checkEvidence {
	result := make([]checkEvidence, len(ids))
	for index, id := range ids {
		result[index] = checkEvidence{ID: id, Passed: true, Detail: "verified"}
	}
	return result
}

func blockedProbes(config suiteConfig) []probeEvidence {
	result := make([]probeEvidence, len(config.Probes))
	for index, spec := range config.Probes {
		result[index] = probeEvidence{
			ID:                     spec.ID,
			Label:                  spec.Label,
			Kind:                   spec.Kind,
			RequiredForPublication: spec.RequiredForPublication,
			Status:                 statusBlocked,
			Reason:                 "test",
			CommandSource:          spec.CommandEnv,
			Checks:                 []checkEvidence{},
			Measurements:           []measurementEvidence{},
			Artifacts:              []artifactEvidence{},
		}
	}
	return result
}

func testTime(t *testing.T) (resultTime time.Time) {
	t.Helper()
	if err := json.Unmarshal([]byte(`"2026-10-10T00:00:00Z"`), &resultTime); err != nil {
		t.Fatal(err)
	}
	return resultTime
}
