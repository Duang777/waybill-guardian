package audit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
)

func TestAppendReplayVerifyAndRedact(t *testing.T) {
	var tick atomic.Int64
	clock := func() time.Time {
		return time.Date(2026, 10, 3, 0, 0, int(tick.Add(1)), 0, time.UTC)
	}
	dir := t.TempDir()
	store, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	runID := domain.RunID("run-a")
	for i := 1; i <= 10; i++ {
		_, err := store.Append(context.Background(), runID, Draft{
			EventID: "event-" + time.Duration(i).String(),
			Actor:   ActorAgent,
			Type:    EventNote,
			Payload: map[string]any{
				"index": i,
				"phone": "13961234567",
				"plate": "川A8X6Q2",
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	events, err := store.Replay(context.Background(), runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 10 {
		t.Fatalf("got %d events, want 10", len(events))
	}
	for index, event := range events {
		if event.Seq != Seq(index+1) {
			t.Fatalf("event %d has seq %d", index, event.Seq)
		}
		if strings.Contains(string(event.Payload), "13961234567") ||
			strings.Contains(string(event.Payload), "川A8X6Q2") {
			t.Fatalf("event %d contains unmasked data: %s", index, event.Payload)
		}
	}
	if err := store.Verify(runID); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := reopened.Replay(context.Background(), runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != len(events) || replayed[9].Hash != events[9].Hash {
		t.Fatalf("reopened journal differs from original")
	}
}

func TestOpenTruncatesIncompleteTail(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	runID := domain.RunID("run-tail")
	if _, err := store.Append(context.Background(), runID, Draft{
		EventID: "one",
		Actor:   ActorSystem,
		Type:    EventRunStarted,
		Payload: map[string]string{"status": "started"},
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "audit-run-tail.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"schema_version":1`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	events, err := reopened.Replay(context.Background(), runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events after repair, want 1", len(events))
	}
}

func TestSubscribeBridgesReplayAndLive(t *testing.T) {
	store, err := Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	runID := domain.RunID("run-stream")
	if _, err := store.Append(context.Background(), runID, Draft{
		EventID: "one",
		Actor:   ActorSystem,
		Type:    EventRunStarted,
		Payload: map[string]string{"status": "started"},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscription, err := store.Subscribe(ctx, runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()

	first := <-subscription.Events()
	if first.Seq != 1 {
		t.Fatalf("first seq = %d, want 1", first.Seq)
	}
	if _, err := store.Append(context.Background(), runID, Draft{
		EventID: "two",
		Actor:   ActorAgent,
		Type:    EventNote,
		Payload: map[string]string{"message": "live"},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case second := <-subscription.Events():
		if second.Seq != 2 {
			t.Fatalf("second seq = %d, want 2", second.Seq)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for live event")
	}
}

func TestMaskHelpers(t *testing.T) {
	if got := MaskPhone("13961234567"); got != "139****4567" {
		t.Fatalf("MaskPhone = %q", got)
	}
	if got := MaskPlate("川A8X6Q2"); got != "川A****2" {
		t.Fatalf("MaskPlate = %q", got)
	}
}
