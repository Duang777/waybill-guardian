package service

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

const FreezeOverrideApprovePermission = "delivery.freeze_override.approve"

type FreezeOverrideApprovalStatus string

const (
	FreezeOverrideApprovalConfirmed FreezeOverrideApprovalStatus = "confirmed"
	FreezeOverrideApprovalPending   FreezeOverrideApprovalStatus = "pending"
	FreezeOverrideApprovalRejected  FreezeOverrideApprovalStatus = "rejected"
	FreezeOverrideApprovalExpired   FreezeOverrideApprovalStatus = "expired"
)

type FreezeOverrideVerificationInput struct {
	Grant                  domain.FreezeOverrideGrant
	Status                 FreezeOverrideApprovalStatus
	ExpectedTenantID       domain.TenantID
	ExpectedPlanID         domain.PlanID
	ExpectedRevisionID     domain.PlanRevisionID
	ExpectedActiveVersion  uint64
	ExpectedProblemDigest  domain.ArtifactDigest
	ExpectedPolicyDigest   domain.ArtifactDigest
	ExpectedFrontierDigest domain.ArtifactDigest
	BaseFrozen             []domain.FrozenTaskCommitment
	BaseExecuted           []domain.ExecutedTaskCommitment
	PlanCreatedBy          string
	ApproverPermissions    []string
	At                     time.Time
}

type VerifiedFreezeOverride struct {
	grant domain.FreezeOverrideGrant
}

func verifyFreezeOverride(
	input FreezeOverrideVerificationInput,
) (*VerifiedFreezeOverride, error) {
	if input.Status != FreezeOverrideApprovalConfirmed {
		return nil, fmt.Errorf("freeze override approval is not confirmed")
	}
	if input.At.IsZero() {
		return nil, fmt.Errorf("freeze override verification time is required")
	}
	grant := input.Grant
	grant.ApprovedAt = grant.ApprovedAt.UTC()
	grant.ExpiresAt = grant.ExpiresAt.UTC()
	for index := range grant.Scopes {
		grant.Scopes[index].Before.PromisedServiceAt =
			grant.Scopes[index].Before.PromisedServiceAt.UTC()
	}
	if grant.SchemaVersion != domain.FreezeOverrideSchemaVersion {
		return nil, fmt.Errorf(
			"freeze override schema_version must be %q",
			domain.FreezeOverrideSchemaVersion,
		)
	}
	if grant.ApprovalID == "" ||
		grant.TenantID == "" ||
		grant.PlanID == "" ||
		grant.BaseRevisionID == "" ||
		grant.BaseActiveVersion == 0 {
		return nil, fmt.Errorf("freeze override identity and binding are required")
	}
	if grant.TenantID != input.ExpectedTenantID ||
		grant.PlanID != input.ExpectedPlanID ||
		grant.BaseRevisionID != input.ExpectedRevisionID ||
		grant.BaseActiveVersion != input.ExpectedActiveVersion ||
		grant.ProblemDigest != input.ExpectedProblemDigest ||
		grant.PolicyDigest != input.ExpectedPolicyDigest ||
		grant.TargetFrontierDigest != input.ExpectedFrontierDigest {
		return nil, fmt.Errorf("freeze override does not match the persisted approval binding")
	}
	if !domain.ValidArtifactDigest(grant.ProblemDigest) ||
		!domain.ValidArtifactDigest(grant.PolicyDigest) ||
		!domain.ValidArtifactDigest(grant.TargetFrontierDigest) {
		return nil, fmt.Errorf("freeze override contains an invalid binding digest")
	}
	if strings.TrimSpace(grant.RequestedBy) == "" ||
		strings.TrimSpace(grant.ApprovedBy) == "" ||
		strings.TrimSpace(grant.Reason) == "" {
		return nil, fmt.Errorf("freeze override actors and reason are required")
	}
	if grant.ApprovedBy == grant.RequestedBy ||
		grant.ApprovedBy == input.PlanCreatedBy {
		return nil, fmt.Errorf("freeze override approval violates separation of duties")
	}
	if !slices.Contains(
		input.ApproverPermissions,
		FreezeOverrideApprovePermission,
	) {
		return nil, fmt.Errorf("freeze override approver lacks required permission")
	}
	if grant.ApprovedAt.IsZero() ||
		grant.ExpiresAt.IsZero() ||
		!grant.ExpiresAt.After(grant.ApprovedAt) ||
		input.At.Before(grant.ApprovedAt) ||
		!input.At.Before(grant.ExpiresAt) {
		return nil, fmt.Errorf("freeze override approval is not valid at verification time")
	}
	if err := validateFreezeOverrideScopes(
		grant.Scopes,
		input.BaseFrozen,
		input.BaseExecuted,
	); err != nil {
		return nil, err
	}
	digest, err := domain.ComputeFreezeOverrideGrantDigest(grant)
	if err != nil {
		return nil, fmt.Errorf("digest freeze override grant: %w", err)
	}
	if !domain.ValidArtifactDigest(grant.Digest) || digest != grant.Digest {
		return nil, fmt.Errorf("freeze override grant digest does not match canonical content")
	}
	grant.Scopes = cloneFreezeOverrideScopes(grant.Scopes)
	return &VerifiedFreezeOverride{grant: grant}, nil
}

func (value *VerifiedFreezeOverride) GrantDigest() domain.ArtifactDigest {
	if value == nil {
		return ""
	}
	return value.grant.Digest
}

func (value *VerifiedFreezeOverride) validateBuild(
	input SuccessorBuildInput,
) error {
	if value == nil {
		return fmt.Errorf("verified freeze override is nil")
	}
	grant := value.grant
	if grant.TenantID != input.BaseProblem.TenantID ||
		grant.PlanID != input.ActivePlan.PlanID ||
		grant.BaseRevisionID != input.ActiveRevisionID ||
		grant.BaseActiveVersion != input.BaseActiveVersion ||
		grant.ProblemDigest != input.BaseProblem.ProblemDigest ||
		grant.PolicyDigest != input.BaseProblem.PolicyDigest ||
		grant.TargetFrontierDigest != input.TargetFrontier.Digest {
		return fmt.Errorf("freeze override does not bind the successor input")
	}
	if !input.CreatedAt.Before(grant.ExpiresAt) {
		return fmt.Errorf("freeze override expired before successor construction")
	}
	return validateFreezeOverrideScopes(
		grant.Scopes,
		input.BaseProblem.Commitments.Frozen,
		input.BaseProblem.Commitments.Executed,
	)
}

func (value *VerifiedFreezeOverride) constraint() *domain.FreezeOverrideConstraint {
	if value == nil {
		return nil
	}
	return &domain.FreezeOverrideConstraint{
		ApprovalID:  value.grant.ApprovalID,
		GrantDigest: value.grant.Digest,
		Scopes:      cloneFreezeOverrideScopes(value.grant.Scopes),
	}
}

func validateFreezeOverrideScopes(
	scopes []domain.FreezeOverrideScope,
	frozen []domain.FrozenTaskCommitment,
	executed []domain.ExecutedTaskCommitment,
) error {
	if len(scopes) == 0 {
		return fmt.Errorf("freeze override must contain at least one bounded scope")
	}
	frozenByTask := make(map[domain.TaskID]domain.FrozenTaskCommitment, len(frozen))
	for _, commitment := range frozen {
		frozenByTask[commitment.TaskID] = commitment
	}
	executedTasks := make(map[domain.TaskID]struct{}, len(executed))
	for _, commitment := range executed {
		executedTasks[commitment.TaskID] = struct{}{}
	}
	scopedCargo := make(map[domain.CargoID]domain.TaskID)
	for index, scope := range scopes {
		if index > 0 && strings.Compare(
			string(scopes[index-1].TaskID),
			string(scope.TaskID),
		) >= 0 {
			return fmt.Errorf("freeze override scopes must be unique and sorted by task_id")
		}
		if scope.TaskID == "" || scope.Before.TaskID != scope.TaskID {
			return fmt.Errorf("freeze override scope %d has an invalid task binding", index)
		}
		if _, done := executedTasks[scope.TaskID]; done {
			return fmt.Errorf("executed task %q cannot be overridden", scope.TaskID)
		}
		expected, exists := frozenByTask[scope.TaskID]
		if !exists || expected != scope.Before {
			return fmt.Errorf(
				"freeze override scope %d does not match the base commitment",
				index,
			)
		}
		if scope.MaxETADriftSeconds < 0 {
			return fmt.Errorf("freeze override scope %d has negative ETA drift", index)
		}
		if err := validateBoundedIDs(
			"vehicle",
			scope.AllowVehicleChange,
			scope.AllowedVehicleIDs,
		); err != nil {
			return fmt.Errorf("freeze override scope %d: %w", index, err)
		}
		if err := validateBoundedIDs(
			"driver",
			scope.AllowDriverChange,
			scope.AllowedDriverIDs,
		); err != nil {
			return fmt.Errorf("freeze override scope %d: %w", index, err)
		}
		if scope.AllowCargoRepack {
			if err := validateSortedNonemptyIDs("cargo", scope.CargoIDs); err != nil {
				return fmt.Errorf("freeze override scope %d: %w", index, err)
			}
			for _, cargoID := range scope.CargoIDs {
				if taskID, duplicate := scopedCargo[cargoID]; duplicate {
					return fmt.Errorf(
						"cargo %q appears in scopes for tasks %q and %q",
						cargoID,
						taskID,
						scope.TaskID,
					)
				}
				scopedCargo[cargoID] = scope.TaskID
			}
			if err := validateSortedNonemptyIDs(
				"compartment",
				scope.AllowedCompartmentIDs,
			); err != nil {
				return fmt.Errorf("freeze override scope %d: %w", index, err)
			}
		} else if len(scope.CargoIDs) > 0 ||
			len(scope.AllowedCompartmentIDs) > 0 {
			return fmt.Errorf(
				"freeze override scope %d has cargo bounds without cargo permission",
				index,
			)
		}
		if !scope.AllowVehicleChange &&
			!scope.AllowDriverChange &&
			scope.MaxSequenceShift == 0 &&
			scope.MaxETADriftSeconds == 0 &&
			!scope.AllowCargoRepack {
			return fmt.Errorf("freeze override scope %d does not permit any change", index)
		}
	}
	return nil
}

func validateBoundedIDs[T ~string](
	name string,
	allowed bool,
	values []T,
) error {
	if !allowed {
		if len(values) > 0 {
			return fmt.Errorf("%s IDs require explicit change permission", name)
		}
		return nil
	}
	return validateSortedNonemptyIDs(name, values)
}

func validateSortedNonemptyIDs[T ~string](name string, values []T) error {
	if len(values) == 0 {
		return fmt.Errorf("allowed %s IDs must not be empty", name)
	}
	for index, value := range values {
		trimmed := strings.TrimSpace(string(value))
		if trimmed == "" || trimmed == "*" {
			return fmt.Errorf("allowed %s ID must be concrete", name)
		}
		if index > 0 && strings.Compare(
			string(values[index-1]),
			string(value),
		) >= 0 {
			return fmt.Errorf("allowed %s IDs must be unique and sorted", name)
		}
	}
	return nil
}

func cloneFreezeOverrideScopes(
	values []domain.FreezeOverrideScope,
) []domain.FreezeOverrideScope {
	result := append([]domain.FreezeOverrideScope(nil), values...)
	for index := range result {
		result[index].AllowedVehicleIDs = append(
			[]domain.VehicleID(nil),
			result[index].AllowedVehicleIDs...,
		)
		result[index].AllowedDriverIDs = append(
			[]domain.DriverID(nil),
			result[index].AllowedDriverIDs...,
		)
		result[index].CargoIDs = append(
			[]domain.CargoID(nil),
			result[index].CargoIDs...,
		)
		result[index].AllowedCompartmentIDs = append(
			[]domain.CompartmentID(nil),
			result[index].AllowedCompartmentIDs...,
		)
	}
	return result
}
