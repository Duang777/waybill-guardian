package solve

import "testing"

func TestEvaluationCounterConsumesExactlyOneUnitPerAcceptedEvaluation(t *testing.T) {
	counter := newEvaluationCounter(2)
	if got, ok := counter.Consume(); !ok || got != 1 {
		t.Fatalf("first Consume() = (%d, %t), want (1, true)", got, ok)
	}
	if got, ok := counter.Consume(); !ok || got != 2 {
		t.Fatalf("second Consume() = (%d, %t), want (2, true)", got, ok)
	}
	if got, ok := counter.Consume(); ok || got != 2 {
		t.Fatalf("third Consume() = (%d, %t), want (2, false)", got, ok)
	}
	if got := counter.Consumed(); got != 2 {
		t.Fatalf("Consumed() = %d, want 2", got)
	}
	if got := counter.Limit(); got != 2 {
		t.Fatalf("Limit() = %d, want 2", got)
	}
	if !counter.Exhausted() {
		t.Fatal("Exhausted() = false, want true")
	}
}
