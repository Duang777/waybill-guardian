package service

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

var ErrNoUnappliedFacts = errors.New("no contiguous unapplied operational facts")

type SuccessorBuildInput struct {
	BaseProblem       domain.ProblemSnapshot
	ActiveRevisionID  domain.PlanRevisionID
	BaseActiveVersion uint64
	ActivePlan        domain.Plan
	BaseFrontier      domain.FactFrontier
	TargetFrontier    domain.FactFrontier
	Facts             []domain.LedgerFact
	Override          *VerifiedFreezeOverride
	CreatedAt         time.Time
}

type SuccessorBuildOutput struct {
	Problem           domain.ProblemSnapshot
	Frontier          domain.FactFrontier
	AppliedFacts      []domain.FactRef
	Applications      []domain.FactApplication
	OverrideUse       *domain.FreezeOverrideUse
	ApplicationDigest domain.ArtifactDigest
}

type ManualReviewError struct {
	Code    string
	Objects []domain.ObjectRef
}

func (value *ManualReviewError) Error() string {
	return fmt.Sprintf(
		"manual review required: %s affects %d protected objects",
		value.Code,
		len(value.Objects),
	)
}

type SemanticFactConflictError struct {
	Key      string
	Existing domain.FactRef
	Incoming domain.FactRef
}

func (value *SemanticFactConflictError) Error() string {
	return fmt.Sprintf(
		"operational facts %q and %q make incompatible claims about %s",
		value.Existing.FactID,
		value.Incoming.FactID,
		value.Key,
	)
}

type factStreamEpoch struct {
	stream domain.FactStream
	epoch  uint64
}

func BuildSuccessor(input SuccessorBuildInput) (SuccessorBuildOutput, error) {
	if input.CreatedAt.IsZero() {
		return SuccessorBuildOutput{}, fmt.Errorf("created_at is required")
	}
	input.CreatedAt = input.CreatedAt.UTC()
	if input.CreatedAt.Before(input.BaseProblem.CreatedAt) {
		return SuccessorBuildOutput{}, fmt.Errorf("successor created_at precedes the base problem")
	}
	if input.BaseActiveVersion == 0 {
		return SuccessorBuildOutput{}, fmt.Errorf("base_active_version must be positive")
	}
	if err := validateActivePlanBinding(input.BaseProblem, input.ActiveRevisionID, input.ActivePlan); err != nil {
		return SuccessorBuildOutput{}, err
	}
	facts, err := validateSuccessorFactCoverage(
		input.BaseFrontier,
		input.TargetFrontier,
		input.Facts,
		input.CreatedAt,
	)
	if err != nil {
		return SuccessorBuildOutput{}, err
	}
	if input.BaseProblem.Commitments.FactWatermark != string(input.BaseFrontier.Digest) {
		return SuccessorBuildOutput{}, fmt.Errorf(
			"base problem fact watermark does not match base frontier",
		)
	}
	if input.Override != nil {
		if err := input.Override.validateBuild(input); err != nil {
			return SuccessorBuildOutput{}, err
		}
	}
	if err := rejectCrossFactTerminalConflicts(input.BaseProblem, facts); err != nil {
		return SuccessorBuildOutput{}, err
	}
	successor, err := cloneProblem(input.BaseProblem)
	if err != nil {
		return SuccessorBuildOutput{}, err
	}
	successor.Version = input.BaseProblem.Version + 1
	successor.CreatedAt = input.CreatedAt
	successor.ProblemDigest = ""
	successor.PolicyDigest = ""
	successor.CommitmentDigest = ""
	context := reducerContext{
		snapshot:  &successor,
		active:    input.ActivePlan,
		createdAt: input.CreatedAt,
		protected: buildProtectedExecutionClosure(
			input.BaseProblem,
			input.ActivePlan,
			input.CreatedAt,
		),
		guardian:     indexFrozenCommitments(input.BaseProblem.Commitments.Frozen),
		projectedETA: make(map[domain.TaskID]time.Time),
		claims:       make(map[string]terminalClaim),
		canceled:     make(map[domain.RequestID]struct{}),
		unloaded:     make(map[domain.CargoID]struct{}),
	}
	applications := make([]domain.FactApplication, 0, len(facts))
	applied := make([]domain.FactRef, 0, len(facts))
	for _, fact := range facts {
		ref := fact.Ref()
		header := fact.Fact.FactHeader()
		successor.SourceRefs = append(
			successor.SourceRefs,
			domain.SourceRef{
				System:       header.Position.Stream.SourceSystem,
				ResourceType: "operational_fact",
				ResourceID:   string(header.FactID),
				Version: fmt.Sprintf(
					"%d:%d:%s",
					header.Position.Epoch,
					header.Position.Sequence,
					fact.Digest,
				),
				ObservedAt: header.ObservedAt,
			},
		)
		application, applyErr := applyOperationalFact(&context, fact)
		if applyErr != nil {
			return SuccessorBuildOutput{}, applyErr
		}
		applications = append(applications, application)
		applied = append(applied, ref)
	}
	successor.Commitments = deriveCommitments(
		successor,
		input.ActivePlan,
		input.CreatedAt,
		context.guardian,
		context.projectedETA,
		context.unloaded,
	)
	successor.Commitments.FactWatermark = string(input.TargetFrontier.Digest)
	if input.Override != nil {
		successor.Commitments.FreezeOverride = input.Override.constraint()
	}
	rebuilt, err := BuildProblemSnapshot(successor)
	if err != nil {
		return SuccessorBuildOutput{}, fmt.Errorf("build successor problem: %w", err)
	}
	applicationDigest, err := domain.ComputeFactApplicationsDigest(applications)
	if err != nil {
		return SuccessorBuildOutput{}, fmt.Errorf("digest fact applications: %w", err)
	}
	return SuccessorBuildOutput{
		Problem:           rebuilt,
		Frontier:          input.TargetFrontier,
		AppliedFacts:      applied,
		Applications:      applications,
		ApplicationDigest: applicationDigest,
	}, nil
}

func validateActivePlanBinding(
	base domain.ProblemSnapshot,
	activeRevisionID domain.PlanRevisionID,
	active domain.Plan,
) error {
	digest, err := domain.ComputePlanDigest(active)
	if err != nil {
		return fmt.Errorf("digest active plan: %w", err)
	}
	if activeRevisionID == "" || active.RevisionID != activeRevisionID ||
		active.SchemaVersion != domain.PlanSchemaVersion ||
		active.ProblemDigest != base.ProblemDigest ||
		active.PolicyDigest != base.PolicyDigest ||
		active.CommitmentDigest != base.CommitmentDigest ||
		active.PlanDigest != digest {
		return fmt.Errorf("active plan is not bound to the base problem and revision")
	}
	return nil
}

func validateSuccessorFactCoverage(
	base domain.FactFrontier,
	target domain.FactFrontier,
	facts []domain.LedgerFact,
	createdAt time.Time,
) ([]domain.LedgerFact, error) {
	if err := domain.ValidateFactFrontier(base); err != nil {
		return nil, fmt.Errorf("base frontier: %w", err)
	}
	if err := domain.ValidateFactFrontier(target); err != nil {
		return nil, fmt.Errorf("target frontier: %w", err)
	}
	baseHeads := frontierHeads(base)
	targetHeads := frontierHeads(target)
	next := make(map[factStreamEpoch]uint64, len(targetHeads))
	advanced := false
	for key, baseHead := range baseHeads {
		targetHead, exists := targetHeads[key]
		if !exists {
			return nil, fmt.Errorf("target frontier omits a base stream")
		}
		if targetHead < baseHead {
			return nil, fmt.Errorf("target frontier regresses a stream")
		}
		if targetHead > baseHead {
			advanced = true
		}
		next[key] = baseHead + 1
	}
	for key, targetHead := range targetHeads {
		if _, exists := baseHeads[key]; !exists {
			next[key] = 1
			if targetHead > 0 {
				advanced = true
			}
		}
	}
	if !advanced {
		return nil, ErrNoUnappliedFacts
	}
	normalized := make([]domain.LedgerFact, len(facts))
	seenFactIDs := make(map[domain.FactID]struct{}, len(facts))
	for index, fact := range facts {
		if fact.Fact == nil {
			return nil, fmt.Errorf("ledger fact %d is nil", index)
		}
		rebuilt, err := domain.BuildLedgerFact(fact.Fact)
		if err != nil {
			return nil, fmt.Errorf("ledger fact %d: %w", index, err)
		}
		if rebuilt.Digest != fact.Digest {
			return nil, fmt.Errorf(
				"ledger fact %q digest does not match canonical content",
				rebuilt.Ref().FactID,
			)
		}
		header := rebuilt.Fact.FactHeader()
		if header.ObservedAt.After(createdAt) {
			return nil, fmt.Errorf(
				"ledger fact %q was observed after successor creation",
				header.FactID,
			)
		}
		if _, duplicate := seenFactIDs[header.FactID]; duplicate {
			return nil, fmt.Errorf("duplicate fact_id %q in successor input", header.FactID)
		}
		seenFactIDs[header.FactID] = struct{}{}
		if index > 0 && compareLedgerFacts(normalized[index-1], rebuilt) >= 0 {
			return nil, fmt.Errorf("ledger facts are not in canonical position order")
		}
		key := factPositionKey(header.Position)
		targetHead, exists := targetHeads[key]
		if !exists {
			return nil, fmt.Errorf("fact %q is outside the target frontier", header.FactID)
		}
		if header.Position.Sequence != next[key] || header.Position.Sequence > targetHead {
			return nil, fmt.Errorf(
				"fact %q does not exactly cover the base-to-target interval",
				header.FactID,
			)
		}
		if next[key] == math.MaxUint64 {
			return nil, fmt.Errorf("fact sequence overflow")
		}
		next[key]++
		normalized[index] = rebuilt
	}
	for key, targetHead := range targetHeads {
		if next[key] != targetHead+1 {
			return nil, fmt.Errorf(
				"facts do not completely cover target stream %q/%q/%q epoch %d",
				key.stream.SourceSystem,
				key.stream.Name,
				key.stream.Partition,
				key.epoch,
			)
		}
	}
	if err := validateSuccessorEpochTransitions(base, target, normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func validateSuccessorEpochTransitions(
	base domain.FactFrontier,
	target domain.FactFrontier,
	facts []domain.LedgerFact,
) error {
	maxBaseEpoch := make(map[domain.FactStream]uint64)
	baseHead := make(map[factStreamEpoch]uint64)
	targetHeads := frontierHeads(target)
	for _, position := range base.Positions {
		key := factPositionKey(position)
		baseHead[key] = position.Sequence
		if position.Epoch > maxBaseEpoch[position.Stream] {
			maxBaseEpoch[position.Stream] = position.Epoch
		}
	}
	maxTargetEpoch := make(map[domain.FactStream]uint64)
	for _, position := range target.Positions {
		if _, exists := baseHead[factPositionKey(position)]; !exists &&
			maxBaseEpoch[position.Stream] > 0 &&
			position.Epoch <= maxBaseEpoch[position.Stream] {
			return fmt.Errorf(
				"target frontier introduces historical epoch %d for stream %q",
				position.Epoch,
				position.Stream.Name,
			)
		}
		if position.Epoch > maxTargetEpoch[position.Stream] {
			maxTargetEpoch[position.Stream] = position.Epoch
		}
	}
	opens := make(map[factStreamEpoch]domain.StreamOpenedFact)
	for _, fact := range facts {
		if opened, ok := fact.Fact.(domain.StreamOpenedFact); ok {
			opens[factPositionKey(opened.Meta.Position)] = opened
		}
	}
	for stream, targetEpoch := range maxTargetEpoch {
		previousEpoch := maxBaseEpoch[stream]
		if previousEpoch == 0 {
			if targetEpoch != 1 {
				return fmt.Errorf("new successor fact stream %q must start at epoch 1", stream.Name)
			}
			if _, exists := opens[factStreamEpoch{stream: stream, epoch: 1}]; exists {
				return fmt.Errorf("epoch 1 must not contain a stream-open fact")
			}
			continue
		}
		if targetEpoch == previousEpoch {
			continue
		}
		if targetEpoch != previousEpoch+1 {
			return fmt.Errorf("successor fact stream %q skipped an epoch", stream.Name)
		}
		previousKey := factStreamEpoch{
			stream: stream,
			epoch:  previousEpoch,
		}
		if targetHeads[previousKey] != baseHead[previousKey] {
			return fmt.Errorf(
				"successor fact stream %q cannot advance epoch %d while opening epoch %d",
				stream.Name,
				previousEpoch,
				targetEpoch,
			)
		}
		opened, exists := opens[factStreamEpoch{stream: stream, epoch: targetEpoch}]
		if !exists ||
			opened.Meta.Position.Sequence != 1 ||
			opened.PreviousEpoch != previousEpoch ||
			opened.PreviousTerminalSequence != baseHead[previousKey] ||
			opened.PreviousFrontierDigest != base.Digest {
			return fmt.Errorf(
				"successor fact stream %q epoch transition is not bound to the base frontier",
				stream.Name,
			)
		}
	}
	return nil
}

func frontierHeads(frontier domain.FactFrontier) map[factStreamEpoch]uint64 {
	result := make(map[factStreamEpoch]uint64, len(frontier.Positions))
	for _, position := range frontier.Positions {
		result[factPositionKey(position)] = position.Sequence
	}
	return result
}

func factPositionKey(position domain.FactPosition) factStreamEpoch {
	return factStreamEpoch{stream: position.Stream, epoch: position.Epoch}
}

func compareLedgerFacts(left, right domain.LedgerFact) int {
	if result := domain.CompareFactPositions(left.Ref().Position, right.Ref().Position); result != 0 {
		return result
	}
	return strings.Compare(string(left.Ref().FactID), string(right.Ref().FactID))
}
