package solve

import (
	"errors"
	"testing"
	"time"
)

func TestBuildSolveConfigProducesReplayIdentityIndependentDigest(t *testing.T) {
	at := time.Date(2026, time.October, 10, 8, 0, 0, 123, time.FixedZone("CST", 8*60*60))
	first, firstDigest, err := BuildSolveConfig(SolveConfig{
		SchemaVersion:    SolveConfigSchemaVersion,
		Strategy:         "deterministic-regret",
		EvaluationBudget: 10_000,
		Seed:             42,
		PlanID:           "plan-1",
		RevisionID:       "revision-1",
		ValidationAt:     at,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, secondDigest, err := BuildSolveConfig(SolveConfig{
		SchemaVersion:    SolveConfigSchemaVersion,
		Strategy:         "deterministic-regret",
		EvaluationBudget: 10_000,
		Seed:             42,
		PlanID:           "plan-2",
		RevisionID:       "revision-2",
		ValidationAt:     at.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	if first.ValidationAt.Location() != time.UTC {
		t.Fatalf("validation_at location = %v, want UTC", first.ValidationAt.Location())
	}
	if firstDigest != secondDigest {
		t.Fatalf("replay-equivalent config digests differ: %q != %q", firstDigest, secondDigest)
	}
	if firstDigest != "a8ed764c8804f823245e64228b118a0fec4356057ad4a4500bc70a7dcc7078c0" {
		t.Fatalf("config digest = %q, want stable protocol digest", firstDigest)
	}
}

func TestBuildSolveConfigRejectsInvalidBoundaryValues(t *testing.T) {
	valid := SolveConfig{
		SchemaVersion:    SolveConfigSchemaVersion,
		Strategy:         "deterministic-regret",
		EvaluationBudget: 100,
		Seed:             1,
		PlanID:           "plan-1",
		RevisionID:       "revision-1",
		ValidationAt:     time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC),
	}
	tests := []struct {
		name   string
		mutate func(*SolveConfig)
	}{
		{name: "schema", mutate: func(value *SolveConfig) { value.SchemaVersion = "v2" }},
		{name: "strategy", mutate: func(value *SolveConfig) { value.Strategy = "" }},
		{name: "budget", mutate: func(value *SolveConfig) { value.EvaluationBudget = 0 }},
		{name: "plan id", mutate: func(value *SolveConfig) { value.PlanID = "" }},
		{name: "revision id", mutate: func(value *SolveConfig) { value.RevisionID = "" }},
		{name: "validation time", mutate: func(value *SolveConfig) { value.ValidationAt = time.Time{} }},
		{name: "route seed digest", mutate: func(value *SolveConfig) {
			value.RouteSeedDigest = "not-a-digest"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.mutate(&value)
			_, _, err := BuildSolveConfig(value)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("BuildSolveConfig error = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestComputeEvidenceDigestIgnoresEnvelopeField(t *testing.T) {
	value := SolveEvidence{
		SchemaVersion: SolveEvidenceVersion,
		Status:        SolveCompleted,
		Evaluations:   17,
	}
	first, err := ComputeEvidenceDigest(value)
	if err != nil {
		t.Fatal(err)
	}
	value.EvidenceDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	second, err := ComputeEvidenceDigest(value)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("evidence digest depends on envelope field: %q != %q", first, second)
	}
}
