package service

import (
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func TestVerifyFreezeOverrideAcceptsExactBoundedGrant(t *testing.T) {
	input := validFreezeOverrideVerification(t)
	verified, err := verifyFreezeOverride(input)
	if err != nil {
		t.Fatal(err)
	}
	if verified.GrantDigest() != input.Grant.Digest {
		t.Fatalf("verified grant digest = %q, want %q",
			verified.GrantDigest(), input.Grant.Digest)
	}
	constraint := verified.constraint()
	input.Grant.Scopes[0].AllowedVehicleIDs[0] = "mutated"
	if constraint.Scopes[0].AllowedVehicleIDs[0] != "vehicle-2" {
		t.Fatalf("verified scope aliases caller input: %+v", constraint.Scopes[0])
	}
}

func TestVerifyFreezeOverrideRejectsUntrustedOrUnboundedGrant(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*FreezeOverrideVerificationInput)
	}{
		{
			name: "not confirmed",
			mutate: func(input *FreezeOverrideVerificationInput) {
				input.Status = FreezeOverrideApprovalPending
			},
		},
		{
			name: "expired",
			mutate: func(input *FreezeOverrideVerificationInput) {
				input.At = input.Grant.ExpiresAt
			},
		},
		{
			name: "same requester and approver",
			mutate: func(input *FreezeOverrideVerificationInput) {
				input.Grant.ApprovedBy = input.Grant.RequestedBy
			},
		},
		{
			name: "plan creator approves",
			mutate: func(input *FreezeOverrideVerificationInput) {
				input.Grant.ApprovedBy = input.PlanCreatedBy
			},
		},
		{
			name: "permission missing",
			mutate: func(input *FreezeOverrideVerificationInput) {
				input.ApproverPermissions = nil
			},
		},
		{
			name: "wildcard vehicle scope",
			mutate: func(input *FreezeOverrideVerificationInput) {
				input.Grant.Scopes[0].AllowedVehicleIDs = []domain.VehicleID{"*"}
			},
		},
		{
			name: "empty bound",
			mutate: func(input *FreezeOverrideVerificationInput) {
				input.Grant.Scopes[0].AllowedVehicleIDs = nil
			},
		},
		{
			name: "executed task",
			mutate: func(input *FreezeOverrideVerificationInput) {
				input.BaseExecuted = []domain.ExecutedTaskCommitment{{
					TaskID:      "delivery-1",
					VehicleID:   "vehicle-1",
					DriverID:    "driver-1",
					CompletedAt: input.At.Add(-time.Minute),
				}}
			},
		},
		{
			name: "base commitment mismatch",
			mutate: func(input *FreezeOverrideVerificationInput) {
				input.Grant.Scopes[0].Before.Sequence++
			},
		},
		{
			name: "tampered digest",
			mutate: func(input *FreezeOverrideVerificationInput) {
				input.Grant.Digest = digestForTest(t, "tampered")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validFreezeOverrideVerification(t)
			test.mutate(&input)
			if test.name != "tampered digest" {
				input.Grant.Digest = ""
				digest, err := domain.ComputeFreezeOverrideGrantDigest(input.Grant)
				if err != nil {
					t.Fatal(err)
				}
				input.Grant.Digest = digest
			}
			if _, err := verifyFreezeOverride(input); err == nil {
				t.Fatal("invalid freeze override was accepted")
			}
		})
	}
}

func validFreezeOverrideVerification(
	t testing.TB,
) FreezeOverrideVerificationInput {
	t.Helper()
	at := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	before := domain.FrozenTaskCommitment{
		TaskID:            "delivery-1",
		VehicleID:         "vehicle-1",
		DriverID:          "driver-1",
		Sequence:          2,
		PromisedServiceAt: at.Add(time.Hour),
		ToleranceSeconds:  300,
	}
	grant := domain.FreezeOverrideGrant{
		SchemaVersion:        domain.FreezeOverrideSchemaVersion,
		ApprovalID:           "approval-override-1",
		TenantID:             "tenant-1",
		PlanID:               "plan-1",
		BaseRevisionID:       "revision-1",
		BaseActiveVersion:    4,
		ProblemDigest:        digestForTest(t, "problem"),
		PolicyDigest:         digestForTest(t, "policy"),
		TargetFrontierDigest: digestForTest(t, "frontier"),
		Scopes: []domain.FreezeOverrideScope{{
			TaskID:             "delivery-1",
			Before:             before,
			AllowVehicleChange: true,
			AllowedVehicleIDs:  []domain.VehicleID{"vehicle-2"},
			MaxSequenceShift:   1,
			MaxETADriftSeconds: 600,
		}},
		RequestedBy: "dispatcher-a",
		ApprovedBy:  "supervisor-b",
		Reason:      "vehicle capacity recovery",
		ApprovedAt:  at.Add(-time.Minute),
		ExpiresAt:   at.Add(time.Hour),
	}
	digest, err := domain.ComputeFreezeOverrideGrantDigest(grant)
	if err != nil {
		t.Fatal(err)
	}
	grant.Digest = digest
	return FreezeOverrideVerificationInput{
		Grant:                  grant,
		Status:                 FreezeOverrideApprovalConfirmed,
		ExpectedTenantID:       grant.TenantID,
		ExpectedPlanID:         grant.PlanID,
		ExpectedRevisionID:     grant.BaseRevisionID,
		ExpectedActiveVersion:  grant.BaseActiveVersion,
		ExpectedProblemDigest:  grant.ProblemDigest,
		ExpectedPolicyDigest:   grant.PolicyDigest,
		ExpectedFrontierDigest: grant.TargetFrontierDigest,
		BaseFrozen:             []domain.FrozenTaskCommitment{before},
		PlanCreatedBy:          "planner-c",
		ApproverPermissions:    []string{FreezeOverrideApprovePermission},
		At:                     at,
	}
}

func digestForTest(t testing.TB, value string) domain.ArtifactDigest {
	t.Helper()
	digest, err := domain.Digest(value)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
