package domain

import (
	"slices"
	"testing"
	"time"
)

func TestBuildFactFrontierIsCanonicalAndTamperEvident(t *testing.T) {
	streamA := FactStream{SourceSystem: "ops", Name: "vehicle", Partition: "a"}
	streamB := FactStream{SourceSystem: "ops", Name: "vehicle", Partition: "b"}
	first, err := BuildFactFrontier([]FactPosition{
		{Stream: streamB, Epoch: 1, Sequence: 4},
		{Stream: streamA, Epoch: 1, Sequence: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildFactFrontier([]FactPosition{
		{Stream: streamA, Epoch: 1, Sequence: 2},
		{Stream: streamB, Epoch: 1, Sequence: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest ||
		!slices.Equal(first.Positions, second.Positions) {
		t.Fatalf("frontiers differ: first=%+v second=%+v", first, second)
	}
	if err := ValidateFactFrontier(first); err != nil {
		t.Fatal(err)
	}
	first.Positions[0].Sequence++
	if err := ValidateFactFrontier(first); err == nil {
		t.Fatal("tampered frontier was accepted")
	}
}

func TestAdvanceFactLedgerRetainsGapsAndPromotesContiguousPrefix(t *testing.T) {
	stream := FactStream{SourceSystem: "ops", Name: "vehicle_soc", Partition: "east"}
	at := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	fact1 := socFact(stream, 1, "fact-1", 1_000, at)
	fact2 := socFact(stream, 2, "fact-2", 900, at.Add(time.Second))
	fact3 := socFact(stream, 3, "fact-3", 800, at.Add(2*time.Second))

	first, err := AdvanceFactLedger(FactLedgerState{}, []OperationalFact{fact3})
	if err != nil {
		t.Fatal(err)
	}
	if first.Decisions[0].Disposition != FactPendingGap ||
		first.Decisions[0].Gap == nil ||
		first.Decisions[0].Gap.Expected != 1 ||
		len(first.State.Frontier.Positions) != 0 {
		t.Fatalf("sequence 3 decision = %+v, frontier = %+v",
			first.Decisions[0], first.State.Frontier)
	}

	second, err := AdvanceFactLedger(first.State, []OperationalFact{fact1})
	if err != nil {
		t.Fatal(err)
	}
	if second.Decisions[0].Disposition != FactAppended ||
		second.State.Frontier.Positions[0].Sequence != 1 {
		t.Fatalf("sequence 1 transition = %+v", second)
	}

	third, err := AdvanceFactLedger(second.State, []OperationalFact{fact2})
	if err != nil {
		t.Fatal(err)
	}
	if third.Decisions[0].Disposition != FactAppended ||
		third.State.Frontier.Positions[0].Sequence != 3 {
		t.Fatalf("gap fill transition = %+v", third)
	}

	replay, err := AdvanceFactLedger(third.State, []OperationalFact{fact2})
	if err != nil {
		t.Fatal(err)
	}
	if replay.Decisions[0].Disposition != FactReplayed ||
		replay.State.Frontier.Digest != third.State.Frontier.Digest ||
		len(replay.State.Facts) != 3 {
		t.Fatalf("replay transition = %+v", replay)
	}
}

func TestAdvanceFactLedgerQuarantinesIdentityConflicts(t *testing.T) {
	stream := FactStream{SourceSystem: "ops", Name: "vehicle_soc", Partition: "east"}
	at := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	original := socFact(stream, 1, "fact-1", 1_000, at)
	first, err := AdvanceFactLedger(FactLedgerState{}, []OperationalFact{original})
	if err != nil {
		t.Fatal(err)
	}
	conflictingPosition := socFact(stream, 1, "fact-other", 900, at)
	conflictingID := socFact(stream, 2, "fact-1", 800, at.Add(time.Second))

	transition, err := AdvanceFactLedger(
		first.State,
		[]OperationalFact{conflictingPosition, conflictingID},
	)
	if err != nil {
		t.Fatal(err)
	}
	for index, decision := range transition.Decisions {
		if decision.Disposition != FactConflict || decision.Conflict == nil {
			t.Fatalf("decision %d = %+v, want conflict", index, decision)
		}
	}
	if len(transition.State.Conflicts) != 2 ||
		len(transition.State.Quarantined) != 1 ||
		transition.State.Frontier.Positions[0].Sequence != 1 ||
		len(transition.State.Facts) != 1 {
		t.Fatalf("conflicted ledger state = %+v", transition.State)
	}
}

func TestAdvanceFactLedgerRejectsMissingRetainedRowBelowFrontier(t *testing.T) {
	stream := FactStream{SourceSystem: "ops", Name: "vehicle_soc", Partition: "east"}
	frontier, err := BuildFactFrontier([]FactPosition{{
		Stream: stream, Epoch: 1, Sequence: 2,
	}})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	fact2, err := BuildLedgerFact(socFact(stream, 2, "fact-2", 900, at))
	if err != nil {
		t.Fatal(err)
	}
	_, err = AdvanceFactLedger(FactLedgerState{
		Frontier: frontier,
		Facts:    []LedgerFact{fact2},
	}, nil)
	if err == nil {
		t.Fatal("ledger corruption was accepted")
	}
}

func TestBuildLedgerFactNormalizesTimestampOffsets(t *testing.T) {
	stream := FactStream{SourceSystem: "ops", Name: "vehicle_soc", Partition: "east"}
	cst := time.FixedZone("CST", 8*60*60)
	at := time.Date(2026, time.October, 10, 16, 0, 0, 0, cst)
	offset, err := BuildLedgerFact(socFact(stream, 1, "fact-1", 1_000, at))
	if err != nil {
		t.Fatal(err)
	}
	utc, err := BuildLedgerFact(socFact(stream, 1, "fact-1", 1_000, at.UTC()))
	if err != nil {
		t.Fatal(err)
	}
	if offset.Digest != utc.Digest {
		t.Fatalf("timestamp offset changed digest: %q != %q", offset.Digest, utc.Digest)
	}
}

func TestAdvanceFactLedgerRequiresBoundStreamEpochTransition(t *testing.T) {
	stream := FactStream{SourceSystem: "ops", Name: "vehicle_soc", Partition: "east"}
	at := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	first, err := AdvanceFactLedger(
		FactLedgerState{},
		[]OperationalFact{socFact(stream, 1, "fact-1", 1_000, at)},
	)
	if err != nil {
		t.Fatal(err)
	}
	unbound := socFact(stream, 1, "fact-epoch-2", 900, at.Add(time.Minute))
	unbound.Meta.Position.Epoch = 2
	if _, err := AdvanceFactLedger(
		first.State,
		[]OperationalFact{unbound},
	); err == nil {
		t.Fatal("unbound epoch transition was accepted")
	}

	opened := StreamOpenedFact{
		Meta: OperationalFactHeader{
			SchemaVersion: OperationalFactSchemaVersion,
			FactID:        "fact-open-2",
			Position: FactPosition{
				Stream: stream, Epoch: 2, Sequence: 1,
			},
			OccurredAt: at.Add(time.Minute),
			ObservedAt: at.Add(time.Minute + time.Second),
		},
		PreviousEpoch:            1,
		PreviousTerminalSequence: 1,
		PreviousFrontierDigest:   first.State.Frontier.Digest,
	}
	second := socFact(stream, 2, "fact-epoch-2", 900, at.Add(2*time.Minute))
	second.Meta.Position.Epoch = 2
	transition, err := AdvanceFactLedger(
		first.State,
		[]OperationalFact{second, opened},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(transition.State.Frontier.Positions) != 2 ||
		transition.State.Frontier.Positions[1].Epoch != 2 ||
		transition.State.Frontier.Positions[1].Sequence != 2 {
		t.Fatalf("epoch frontier = %+v", transition.State.Frontier)
	}

	opened.PreviousFrontierDigest = transition.State.Frontier.Digest
	opened.Meta.FactID = "fact-open-tampered"
	if _, err := AdvanceFactLedger(
		first.State,
		[]OperationalFact{opened},
	); err == nil {
		t.Fatal("stream-open with wrong prior frontier was accepted")
	}
}

func TestAdvanceFactLedgerRejectsPriorEpochAdvanceBundledWithOpen(t *testing.T) {
	stream := FactStream{SourceSystem: "ops", Name: "vehicle_soc", Partition: "east"}
	at := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	first, err := AdvanceFactLedger(
		FactLedgerState{},
		[]OperationalFact{socFact(stream, 1, "fact-1", 1_000, at)},
	)
	if err != nil {
		t.Fatal(err)
	}
	opened := StreamOpenedFact{
		Meta: OperationalFactHeader{
			SchemaVersion: OperationalFactSchemaVersion,
			FactID:        "fact-open-2",
			Position: FactPosition{
				Stream: stream, Epoch: 2, Sequence: 1,
			},
			OccurredAt: at.Add(2 * time.Minute),
			ObservedAt: at.Add(2*time.Minute + time.Second),
		},
		PreviousEpoch:            1,
		PreviousTerminalSequence: 1,
		PreviousFrontierDigest:   first.State.Frontier.Digest,
	}
	priorAdvance := socFact(stream, 2, "fact-2", 900, at.Add(time.Minute))

	if _, err := AdvanceFactLedger(
		first.State,
		[]OperationalFact{priorAdvance, opened},
	); err == nil {
		t.Fatal("epoch open bundled with a prior-epoch advance was accepted")
	}
}

func socFact(
	stream FactStream,
	sequence uint64,
	id FactID,
	soc int64,
	at time.Time,
) VehicleSOCObservedFact {
	return VehicleSOCObservedFact{
		Meta: OperationalFactHeader{
			SchemaVersion: OperationalFactSchemaVersion,
			FactID:        id,
			Position: FactPosition{
				Stream:   stream,
				Epoch:    1,
				Sequence: sequence,
			},
			OccurredAt: at,
			ObservedAt: at.Add(time.Second),
		},
		VehicleID: "vehicle-1",
		SOCWh:     soc,
	}
}
