package domain

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestLoadStageJSONUsesExplicitRehandleOperations(t *testing.T) {
	placement := Placement{
		CargoID:       "cargo-1",
		CompartmentID: "compartment-1",
		DoorID:        "door-1",
	}
	stage := LoadStage{
		AfterStopIndex: 2,
		Placements:     []Placement{placement},
		AxleLoadsG:     []int64{},
		Rehandles: []RehandleOperation{{
			Sequence:        1,
			CargoID:         "cargo-1",
			StopIndex:       2,
			Before:          placement,
			After:           placement,
			DurationSeconds: 90,
			CostCents:       250,
		}},
	}

	raw, err := json.Marshal(stage)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range [][]byte{
		[]byte(`"rehandles"`),
		[]byte(`"before"`),
		[]byte(`"after"`),
		[]byte(`"duration_seconds":90`),
		[]byte(`"cost_cents":250`),
	} {
		if !bytes.Contains(raw, field) {
			t.Fatalf("load stage JSON %s lacks %s", raw, field)
		}
	}
	if bytes.Contains(raw, []byte(`"rehandled_cargo"`)) {
		t.Fatalf("load stage JSON still exposes legacy rehandled_cargo: %s", raw)
	}

	var roundTrip LoadStage
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if len(roundTrip.Rehandles) != 1 ||
		roundTrip.Rehandles[0] != stage.Rehandles[0] {
		t.Fatalf("round-trip rehandles = %+v, want %+v",
			roundTrip.Rehandles, stage.Rehandles)
	}
}
