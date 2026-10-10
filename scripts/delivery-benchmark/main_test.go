package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/delivery/validate"
)

func TestSyntheticDatasetAndReferencePlanAreDeterministic(t *testing.T) {
	config := testConfig(t, 4)
	firstDatasets, firstManifest, err := generateDatasets(config)
	if err != nil {
		t.Fatal(err)
	}
	secondDatasets, secondManifest, err := generateDatasets(config)
	if err != nil {
		t.Fatal(err)
	}
	firstRaw, err := marshalIndented(firstManifest)
	if err != nil {
		t.Fatal(err)
	}
	secondRaw, err := marshalIndented(secondManifest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstRaw, secondRaw) {
		t.Fatal("manifest changed across identical generation")
	}
	if !bytes.Equal(firstDatasets[0].canonical, secondDatasets[0].canonical) {
		t.Fatal("canonical problem changed across identical generation")
	}

	changed := config
	changed.Datasets = append([]datasetSpec(nil), config.Datasets...)
	changed.Datasets[0].Seed++
	changedDatasets, _, err := generateDatasets(changed)
	if err != nil {
		t.Fatal(err)
	}
	if changedDatasets[0].problem.ProblemDigest == firstDatasets[0].problem.ProblemDigest {
		t.Fatal("changing the seed did not change the problem digest")
	}
}

func TestReferencePlanPassesTwentyIndependentValidatorReplays(t *testing.T) {
	config := testConfig(t, 8)
	datasets, _, err := generateDatasets(config)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := buildReferencePlan(datasets[0].problem, datasets[0].spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	validator := validate.New(config.Validator)
	createdAt := time.Date(2026, time.October, 10, 7, 59, 0, 0, time.UTC)
	var reportDigest domain.ArtifactDigest
	for iteration := 0; iteration < 20; iteration++ {
		report := validator.Validate(datasets[0].problem, plan, createdAt)
		if !report.Valid || countHardViolations(report) != 0 {
			t.Fatalf("replay %d rejected the reference plan: %+v", iteration+1, report.Violations)
		}
		if iteration == 0 {
			reportDigest = report.ReportDigest
		} else if report.ReportDigest != reportDigest {
			t.Fatalf(
				"replay %d report digest = %q, want %q",
				iteration+1,
				report.ReportDigest,
				reportDigest,
			)
		}
	}
}

func TestReportVerifierRejectsReplayDigestDrift(t *testing.T) {
	config := testConfig(t, 2)
	datasets, manifest, err := generateDatasets(config)
	if err != nil {
		t.Fatal(err)
	}
	configRaw, err := marshalIndented(config)
	if err != nil {
		t.Fatal(err)
	}
	manifestRaw, err := marshalIndented(manifest)
	if err != nil {
		t.Fatal(err)
	}
	root, err := repositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	report := runBenchmark(
		context.Background(),
		root,
		config,
		configRaw,
		datasets,
		manifest,
		manifestRaw,
	)
	if err := verifyReport(root, report, config, configRaw, manifest, manifestRaw); err != nil {
		t.Fatalf("valid report rejected: %v", err)
	}
	report.Adapters[0].Datasets[0].Runs[19].PlanDigest =
		domain.ArtifactDigest(strings.Repeat("f", 64))
	if err := verifyReport(root, report, config, configRaw, manifest, manifestRaw); err == nil {
		t.Fatal("report with replay digest drift was accepted")
	}
}

func TestCommandTemplateRequiresProblemAndPlanPaths(t *testing.T) {
	if err := validateCommandTemplate([]string{"solver", "--input", "{problem}"}); err == nil {
		t.Fatal("command without a plan output was accepted")
	}
	command := []string{
		"solver",
		"--input={problem}",
		"--output={plan}",
		"--budget={budget}",
	}
	if err := validateCommandTemplate(command); err != nil {
		t.Fatal(err)
	}
	expanded := expandCommand(command, map[string]string{
		"{problem}": "/tmp/problem.json",
		"{plan}":    "/tmp/plan.json",
		"{budget}":  "100",
	})
	if strings.Join(expanded, " ") !=
		"solver --input=/tmp/problem.json --output=/tmp/plan.json --budget=100" {
		t.Fatalf("expanded command = %q", expanded)
	}
}

func TestStrictJSONRejectsTrailingValuesAndGarbage(t *testing.T) {
	for _, raw := range []string{
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

func TestGitHubSlugPreservesChineseAndDeduplicatesWhitespace(t *testing.T) {
	if got := githubSlug("60 秒配音稿"); got != "60-秒配音稿" {
		t.Fatalf("slug = %q", got)
	}
	if got := githubSlug("HTTP / SSE reference"); got != "http-sse-reference" {
		t.Fatalf("slug = %q", got)
	}
}

func TestExternalLinkCheckSkipsOnlyLoopbackHosts(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1", "::1"} {
		if !isLoopbackHost(host) {
			t.Fatalf("%q was not recognized as loopback", host)
		}
	}
	if isLoopbackHost("example.com") {
		t.Fatal("public host was recognized as loopback")
	}
}

func testConfig(t *testing.T, requestCount int) suiteConfig {
	t.Helper()
	config, _, err := loadConfig("config.json")
	if err != nil {
		t.Fatal(err)
	}
	config.Datasets = []datasetSpec{{
		ID:           "test",
		Seed:         42,
		RequestCount: requestCount,
	}}
	config.Adapters = []adapterSpec{config.Adapters[0]}
	return config
}
