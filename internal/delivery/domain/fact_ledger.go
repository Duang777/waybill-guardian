package domain

import (
	"fmt"
	"slices"
	"strings"
)

type factStreamEpoch struct {
	Stream FactStream
	Epoch  uint64
}

func BuildLedgerFact(fact OperationalFact) (LedgerFact, error) {
	normalized, err := normalizeOperationalFact(fact)
	if err != nil {
		return LedgerFact{}, err
	}
	if validationErr := validateOperationalFactHeader(
		normalized.FactHeader(),
	); validationErr != nil {
		return LedgerFact{}, validationErr
	}
	digest, err := Digest(normalized)
	if err != nil {
		return LedgerFact{}, fmt.Errorf("digest operational fact: %w", err)
	}
	return LedgerFact{Fact: normalized, Digest: digest}, nil
}

func BuildFactFrontier(positions []FactPosition) (FactFrontier, error) {
	normalized := append([]FactPosition(nil), positions...)
	slices.SortFunc(normalized, CompareFactPositions)
	for index, position := range normalized {
		if err := validateFactPosition(position); err != nil {
			return FactFrontier{}, fmt.Errorf("frontier position %d: %w", index, err)
		}
		if index > 0 && sameFactStreamEpoch(normalized[index-1], position) {
			return FactFrontier{}, fmt.Errorf(
				"frontier contains duplicate stream %q/%q/%q epoch %d",
				position.Stream.SourceSystem,
				position.Stream.Name,
				position.Stream.Partition,
				position.Epoch,
			)
		}
	}
	frontier := FactFrontier{
		SchemaVersion: FactFrontierSchemaVersion,
		Positions:     normalized,
	}
	digest, err := computeFactFrontierDigest(frontier)
	if err != nil {
		return FactFrontier{}, err
	}
	frontier.Digest = digest
	return frontier, nil
}

func ValidateFactFrontier(frontier FactFrontier) error {
	if frontier.SchemaVersion != FactFrontierSchemaVersion {
		return fmt.Errorf("fact frontier schema_version must be %q", FactFrontierSchemaVersion)
	}
	if !ValidArtifactDigest(frontier.Digest) {
		return fmt.Errorf("fact frontier digest is invalid")
	}
	rebuilt, err := BuildFactFrontier(frontier.Positions)
	if err != nil {
		return err
	}
	if !slices.Equal(frontier.Positions, rebuilt.Positions) {
		return fmt.Errorf("fact frontier positions are not in canonical order")
	}
	if frontier.Digest != rebuilt.Digest {
		return fmt.Errorf("fact frontier digest does not match canonical positions")
	}
	return nil
}

func CompareFactPositions(left, right FactPosition) int {
	if result := compareFactStreams(left.Stream, right.Stream); result != 0 {
		return result
	}
	if left.Epoch < right.Epoch {
		return -1
	}
	if left.Epoch > right.Epoch {
		return 1
	}
	if left.Sequence < right.Sequence {
		return -1
	}
	if left.Sequence > right.Sequence {
		return 1
	}
	return 0
}

func ComputeFactApplicationsDigest(values []FactApplication) (ArtifactDigest, error) {
	return Digest(values)
}

func ComputeFreezeOverrideGrantDigest(value FreezeOverrideGrant) (ArtifactDigest, error) {
	value.Digest = ""
	value.ApprovedAt = value.ApprovedAt.UTC()
	value.ExpiresAt = value.ExpiresAt.UTC()
	for index := range value.Scopes {
		value.Scopes[index].Before.PromisedServiceAt =
			value.Scopes[index].Before.PromisedServiceAt.UTC()
	}
	return Digest(value)
}

func ComputeFreezeOverrideUseDigest(value FreezeOverrideUse) (ArtifactDigest, error) {
	value.Digest = ""
	for index := range value.TaskChanges {
		value.TaskChanges[index].Before.PromisedServiceAt =
			value.TaskChanges[index].Before.PromisedServiceAt.UTC()
		value.TaskChanges[index].After.PromisedServiceAt =
			value.TaskChanges[index].After.PromisedServiceAt.UTC()
	}
	return Digest(value)
}

func AdvanceFactLedger(
	state FactLedgerState,
	incoming []OperationalFact,
) (FactLedgerTransition, error) {
	normalized, positionIndex, factIDIndex, err := normalizeLedgerState(state)
	if err != nil {
		return FactLedgerTransition{}, err
	}
	decisions := make([]FactLedgerDecision, len(incoming))
	inserted := make([]bool, len(incoming))
	conflictKeys := make(map[string]struct{}, len(normalized.Conflicts))
	for _, conflict := range normalized.Conflicts {
		conflictKeys[factConflictKey(conflict)] = struct{}{}
	}
	quarantined := make(map[FactStream]struct{}, len(normalized.Quarantined))
	for _, stream := range normalized.Quarantined {
		quarantined[stream] = struct{}{}
	}

	incomingFacts := make([]LedgerFact, len(incoming))
	for index, fact := range incoming {
		ledgerFact, buildErr := BuildLedgerFact(fact)
		if buildErr != nil {
			return FactLedgerTransition{}, fmt.Errorf(
				"operational fact %d: %w",
				index,
				buildErr,
			)
		}
		incomingFacts[index] = ledgerFact
	}
	if err := validateLedgerEpochTransitions(
		normalized,
		incomingFacts,
	); err != nil {
		return FactLedgerTransition{}, err
	}

	for index, ledgerFact := range incomingFacts {
		ref := ledgerFact.Ref()
		decisions[index].Fact = ref

		if existing, exists := positionIndex[ref.Position]; exists {
			if existing.Ref() == ref {
				decisions[index].Disposition = FactReplayed
				continue
			}
			record := factConflict(
				existing.Ref(),
				ref,
				"position_reused",
			)
			decisions[index].Disposition = FactConflict
			decisions[index].Conflict = &record
			if _, duplicate := conflictKeys[factConflictKey(record)]; !duplicate {
				normalized.Conflicts = append(normalized.Conflicts, record)
				conflictKeys[factConflictKey(record)] = struct{}{}
			}
			quarantined[ref.Position.Stream] = struct{}{}
			continue
		}
		if existing, exists := factIDIndex[ref.FactID]; exists {
			record := factConflict(
				existing.Ref(),
				ref,
				"fact_id_reused",
			)
			decisions[index].Disposition = FactConflict
			decisions[index].Conflict = &record
			if _, duplicate := conflictKeys[factConflictKey(record)]; !duplicate {
				normalized.Conflicts = append(normalized.Conflicts, record)
				conflictKeys[factConflictKey(record)] = struct{}{}
			}
			quarantined[ref.Position.Stream] = struct{}{}
			quarantined[existing.Ref().Position.Stream] = struct{}{}
			continue
		}

		normalized.Facts = append(normalized.Facts, ledgerFact)
		positionIndex[ref.Position] = ledgerFact
		factIDIndex[ref.FactID] = ledgerFact
		inserted[index] = true
	}

	heads := make(map[factStreamEpoch]uint64, len(normalized.Frontier.Positions))
	for _, position := range normalized.Frontier.Positions {
		heads[streamEpoch(position)] = position.Sequence
	}
	for _, fact := range normalized.Facts {
		key := streamEpoch(fact.Ref().Position)
		if _, exists := heads[key]; !exists {
			heads[key] = 0
		}
	}
	for key, head := range heads {
		if _, blocked := quarantined[key.Stream]; blocked {
			continue
		}
		for head < ^uint64(0) {
			next := FactPosition{
				Stream:   key.Stream,
				Epoch:    key.Epoch,
				Sequence: head + 1,
			}
			if _, exists := positionIndex[next]; !exists {
				break
			}
			head++
		}
		heads[key] = head
	}

	positions := make([]FactPosition, 0, len(heads))
	for key, head := range heads {
		if head == 0 {
			continue
		}
		positions = append(positions, FactPosition{
			Stream:   key.Stream,
			Epoch:    key.Epoch,
			Sequence: head,
		})
	}
	normalized.Frontier, err = BuildFactFrontier(positions)
	if err != nil {
		return FactLedgerTransition{}, err
	}

	for index := range decisions {
		if !inserted[index] {
			continue
		}
		ref := decisions[index].Fact
		head := heads[streamEpoch(ref.Position)]
		if _, blocked := quarantined[ref.Position.Stream]; !blocked &&
			ref.Position.Sequence <= head {
			decisions[index].Disposition = FactAppended
			continue
		}
		decisions[index].Disposition = FactPendingGap
		gap := FactGap{
			Stream:   ref.Position.Stream,
			Epoch:    ref.Position.Epoch,
			Expected: head + 1,
			Observed: ref.Position.Sequence,
		}
		decisions[index].Gap = &gap
	}

	normalized.Quarantined = normalized.Quarantined[:0]
	for stream := range quarantined {
		normalized.Quarantined = append(normalized.Quarantined, stream)
	}
	slices.SortFunc(normalized.Quarantined, compareFactStreams)
	slices.SortFunc(normalized.Facts, compareLedgerFacts)
	slices.SortFunc(normalized.Conflicts, compareFactConflicts)
	return FactLedgerTransition{
		State:     normalized,
		Decisions: decisions,
	}, nil
}

func normalizeLedgerState(
	state FactLedgerState,
) (
	FactLedgerState,
	map[FactPosition]LedgerFact,
	map[FactID]LedgerFact,
	error,
) {
	if state.Frontier.SchemaVersion == "" &&
		state.Frontier.Digest == "" &&
		len(state.Frontier.Positions) == 0 {
		empty, err := BuildFactFrontier(nil)
		if err != nil {
			return FactLedgerState{}, nil, nil, err
		}
		state.Frontier = empty
	}
	if err := ValidateFactFrontier(state.Frontier); err != nil {
		return FactLedgerState{}, nil, nil, err
	}
	state.Facts = append([]LedgerFact(nil), state.Facts...)
	state.Conflicts = append([]FactConflictRecord(nil), state.Conflicts...)
	state.Quarantined = append([]FactStream(nil), state.Quarantined...)

	positionIndex := make(map[FactPosition]LedgerFact, len(state.Facts))
	factIDIndex := make(map[FactID]LedgerFact, len(state.Facts))
	covered := make(map[factStreamEpoch]uint64, len(state.Frontier.Positions))
	for index, fact := range state.Facts {
		rebuilt, err := BuildLedgerFact(fact.Fact)
		if err != nil {
			return FactLedgerState{}, nil, nil, fmt.Errorf(
				"ledger fact %d: %w",
				index,
				err,
			)
		}
		if rebuilt.Digest != fact.Digest {
			return FactLedgerState{}, nil, nil, fmt.Errorf(
				"ledger fact %q digest does not match canonical content",
				rebuilt.Ref().FactID,
			)
		}
		ref := rebuilt.Ref()
		if _, duplicate := positionIndex[ref.Position]; duplicate {
			return FactLedgerState{}, nil, nil, fmt.Errorf(
				"ledger contains duplicate fact position",
			)
		}
		if _, duplicate := factIDIndex[ref.FactID]; duplicate {
			return FactLedgerState{}, nil, nil, fmt.Errorf(
				"ledger contains duplicate fact_id %q",
				ref.FactID,
			)
		}
		state.Facts[index] = rebuilt
		positionIndex[ref.Position] = rebuilt
		factIDIndex[ref.FactID] = rebuilt
	}
	for _, head := range state.Frontier.Positions {
		key := streamEpoch(head)
		for position := range positionIndex {
			if streamEpoch(position) == key && position.Sequence <= head.Sequence {
				covered[key]++
			}
		}
		if covered[key] != head.Sequence {
			return FactLedgerState{}, nil, nil, fmt.Errorf(
				"ledger is missing a retained row below frontier for stream %q/%q/%q epoch %d",
				head.Stream.SourceSystem,
				head.Stream.Name,
				head.Stream.Partition,
				head.Epoch,
			)
		}
	}
	return state, positionIndex, factIDIndex, nil
}

func validateOperationalFactHeader(header OperationalFactHeader) error {
	if header.SchemaVersion != OperationalFactSchemaVersion {
		return fmt.Errorf(
			"schema_version must be %q",
			OperationalFactSchemaVersion,
		)
	}
	if strings.TrimSpace(string(header.FactID)) == "" {
		return fmt.Errorf("fact_id is required")
	}
	if err := validateFactPosition(header.Position); err != nil {
		return err
	}
	if header.OccurredAt.IsZero() || header.ObservedAt.IsZero() {
		return fmt.Errorf("occurred_at and observed_at are required")
	}
	if header.ObservedAt.Before(header.OccurredAt) {
		return fmt.Errorf("observed_at must not precede occurred_at")
	}
	return nil
}

func validateFactPosition(position FactPosition) error {
	if strings.TrimSpace(position.Stream.SourceSystem) == "" ||
		strings.TrimSpace(position.Stream.Name) == "" ||
		strings.TrimSpace(position.Stream.Partition) == "" {
		return fmt.Errorf("fact stream source_system, name, and partition are required")
	}
	if position.Epoch == 0 || position.Sequence == 0 {
		return fmt.Errorf("fact epoch and sequence must be positive")
	}
	return nil
}

func normalizeOperationalFact(fact OperationalFact) (OperationalFact, error) {
	if fact == nil {
		return nil, fmt.Errorf("operational fact is nil")
	}
	switch value := fact.(type) {
	case StreamOpenedFact:
		value.Meta = normalizeFactHeader(value.Meta)
		return value, nil
	case NewRequestFact:
		value.Meta = normalizeFactHeader(value.Meta)
		value.SourceRef.ObservedAt = value.SourceRef.ObservedAt.UTC()
		return value, nil
	case RequestCanceledFact:
		value.Meta = normalizeFactHeader(value.Meta)
		return value, nil
	case TaskCompletedFact:
		value.Meta = normalizeFactHeader(value.Meta)
		value.CompletedAt = value.CompletedAt.UTC()
		return value, nil
	case VehicleUnavailableFact:
		value.Meta = normalizeFactHeader(value.Meta)
		value.UnavailableFrom = value.UnavailableFrom.UTC()
		return value, nil
	case DriverUnavailableFact:
		value.Meta = normalizeFactHeader(value.Meta)
		value.UnavailableFrom = value.UnavailableFrom.UTC()
		return value, nil
	case ChargerUnavailableFact:
		value.Meta = normalizeFactHeader(value.Meta)
		value.UnavailableFrom = value.UnavailableFrom.UTC()
		return value, nil
	case TravelMatrixChangedFact:
		value.Meta = normalizeFactHeader(value.Meta)
		return value, nil
	case VehicleSOCObservedFact:
		value.Meta = normalizeFactHeader(value.Meta)
		return value, nil
	case ETADeviationFact:
		value.Meta = normalizeFactHeader(value.Meta)
		value.ProjectedServiceAt = value.ProjectedServiceAt.UTC()
		return value, nil
	case GuardianAssignmentFact:
		value.Meta = normalizeFactHeader(value.Meta)
		value.PromisedServiceAt = value.PromisedServiceAt.UTC()
		return value, nil
	default:
		return nil, fmt.Errorf("unsupported operational fact type %T", fact)
	}
}

func validateLedgerEpochTransitions(
	state FactLedgerState,
	incoming []LedgerFact,
) error {
	type streamState struct {
		maxEpoch uint64
		heads    map[uint64]uint64
		maxRows  map[uint64]uint64
	}
	streams := make(map[FactStream]*streamState)
	get := func(stream FactStream) *streamState {
		current := streams[stream]
		if current == nil {
			current = &streamState{
				heads:   make(map[uint64]uint64),
				maxRows: make(map[uint64]uint64),
			}
			streams[stream] = current
		}
		return current
	}
	for _, position := range state.Frontier.Positions {
		current := get(position.Stream)
		current.heads[position.Epoch] = position.Sequence
		if position.Epoch > current.maxEpoch {
			current.maxEpoch = position.Epoch
		}
	}
	for _, fact := range state.Facts {
		position := fact.Ref().Position
		current := get(position.Stream)
		if position.Sequence > current.maxRows[position.Epoch] {
			current.maxRows[position.Epoch] = position.Sequence
		}
		if position.Epoch > current.maxEpoch {
			current.maxEpoch = position.Epoch
		}
	}

	opens := make(map[factStreamEpoch]StreamOpenedFact)
	maxIncomingEpoch := make(map[FactStream]uint64)
	for _, fact := range incoming {
		position := fact.Ref().Position
		if position.Epoch > maxIncomingEpoch[position.Stream] {
			maxIncomingEpoch[position.Stream] = position.Epoch
		}
		if opened, ok := fact.Fact.(StreamOpenedFact); ok {
			if position.Sequence != 1 {
				return fmt.Errorf(
					"stream-open fact %q must use sequence 1",
					position.Stream.Name,
				)
			}
			opens[streamEpoch(position)] = opened
		}
	}
	for stream, incomingEpoch := range maxIncomingEpoch {
		current := get(stream)
		switch {
		case current.maxEpoch == 0:
			if incomingEpoch != 1 {
				return fmt.Errorf(
					"new fact stream %q must start at epoch 1",
					stream.Name,
				)
			}
			if _, hasOpen := opens[factStreamEpoch{
				Stream: stream,
				Epoch:  1,
			}]; hasOpen {
				return fmt.Errorf("epoch 1 must not contain a stream-open fact")
			}
		case incomingEpoch > current.maxEpoch:
			if incomingEpoch != current.maxEpoch+1 {
				return fmt.Errorf(
					"fact stream %q skipped from epoch %d to %d",
					stream.Name,
					current.maxEpoch,
					incomingEpoch,
				)
			}
			for _, fact := range incoming {
				position := fact.Ref().Position
				if position.Stream == stream &&
					position.Epoch <= current.maxEpoch &&
					position.Sequence > current.heads[position.Epoch] {
					return fmt.Errorf(
						"fact stream %q cannot advance epoch %d while opening epoch %d",
						stream.Name,
						position.Epoch,
						incomingEpoch,
					)
				}
			}
			head := current.heads[current.maxEpoch]
			if current.maxRows[current.maxEpoch] > head {
				return fmt.Errorf(
					"fact stream %q cannot change epoch while prior facts are pending",
					stream.Name,
				)
			}
			opened, exists := opens[factStreamEpoch{
				Stream: stream,
				Epoch:  incomingEpoch,
			}]
			if !exists ||
				opened.PreviousEpoch != current.maxEpoch ||
				opened.PreviousTerminalSequence != head ||
				opened.PreviousFrontierDigest != state.Frontier.Digest {
				return fmt.Errorf(
					"fact stream %q epoch transition is not bound to the prior frontier",
					stream.Name,
				)
			}
		}
		for _, fact := range incoming {
			position := fact.Ref().Position
			if position.Stream == stream &&
				position.Epoch < current.maxEpoch &&
				position.Sequence > current.heads[position.Epoch] {
				return fmt.Errorf(
					"fact stream %q epoch %d is already closed",
					stream.Name,
					position.Epoch,
				)
			}
		}
	}
	return nil
}

func normalizeFactHeader(header OperationalFactHeader) OperationalFactHeader {
	header.OccurredAt = header.OccurredAt.UTC()
	header.ObservedAt = header.ObservedAt.UTC()
	return header
}

func computeFactFrontierDigest(frontier FactFrontier) (ArtifactDigest, error) {
	return Digest(struct {
		SchemaVersion string         `json:"schema_version"`
		Positions     []FactPosition `json:"positions"`
	}{
		SchemaVersion: frontier.SchemaVersion,
		Positions:     frontier.Positions,
	})
}

func compareFactStreams(left, right FactStream) int {
	if result := strings.Compare(left.SourceSystem, right.SourceSystem); result != 0 {
		return result
	}
	if result := strings.Compare(left.Name, right.Name); result != 0 {
		return result
	}
	return strings.Compare(left.Partition, right.Partition)
}

func compareLedgerFacts(left, right LedgerFact) int {
	if result := CompareFactPositions(left.Ref().Position, right.Ref().Position); result != 0 {
		return result
	}
	return strings.Compare(string(left.Ref().FactID), string(right.Ref().FactID))
}

func compareFactConflicts(left, right FactConflictRecord) int {
	if result := CompareFactPositions(left.Position, right.Position); result != 0 {
		return result
	}
	if result := strings.Compare(string(left.IncomingFactID), string(right.IncomingFactID)); result != 0 {
		return result
	}
	return strings.Compare(left.Code, right.Code)
}

func sameFactStreamEpoch(left, right FactPosition) bool {
	return left.Stream == right.Stream && left.Epoch == right.Epoch
}

func streamEpoch(position FactPosition) factStreamEpoch {
	return factStreamEpoch{Stream: position.Stream, Epoch: position.Epoch}
}

func factConflict(
	existing FactRef,
	incoming FactRef,
	code string,
) FactConflictRecord {
	return FactConflictRecord{
		Position:       incoming.Position,
		ExistingFactID: existing.FactID,
		IncomingFactID: incoming.FactID,
		ExistingDigest: existing.Digest,
		IncomingDigest: incoming.Digest,
		Code:           code,
	}
}

func factConflictKey(value FactConflictRecord) string {
	return fmt.Sprintf(
		"%s/%s/%s/%d/%d:%s:%s:%s:%s:%s",
		value.Position.Stream.SourceSystem,
		value.Position.Stream.Name,
		value.Position.Stream.Partition,
		value.Position.Epoch,
		value.Position.Sequence,
		value.ExistingFactID,
		value.IncomingFactID,
		value.ExistingDigest,
		value.IncomingDigest,
		value.Code,
	)
}
