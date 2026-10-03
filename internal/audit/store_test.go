package audit

import (
	"context"
	"errors"
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
	defer store.Close()
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
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
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
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	events, err := reopened.Replay(context.Background(), runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events after repair, want 1", len(events))
	}
}

func TestOpenRejectsSecondWriter(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}

	second, err := Open(dir, time.Now)
	if second != nil {
		_ = second.Close()
		t.Fatal("second writer unexpectedly opened the same directory")
	}
	if !errors.Is(err, ErrWriterLocked) {
		t.Fatalf("second Open error = %v, want ErrWriterLocked", err)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir, time.Now)
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	defer reopened.Close()
}

func TestOpenSecuresDataDirectoryAndLockFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(dir, "audit-existing.jsonl")
	if err := os.WriteFile(journalPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	assertMode(t, dir, 0o700)
	assertMode(t, filepath.Join(dir, ".writer.lock"), 0o600)
	assertMode(t, journalPath, 0o600)
}

func TestSubscribeBridgesReplayAndLive(t *testing.T) {
	store, err := Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
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

func TestClosedStoreRejectsOperationsAndClosesSubscriptions(t *testing.T) {
	store, err := Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	runID := domain.RunID("run-closed")
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
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, open := <-subscription.Events(); !open {
		t.Fatal("subscription closed before buffered replay was drained")
	}
	if _, open := <-subscription.Events(); open {
		t.Fatal("subscription remained open after store close")
	}
	if _, err := store.Append(context.Background(), runID, Draft{
		EventID: "two",
		Actor:   ActorSystem,
		Type:    EventNote,
	}); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("Append after Close error = %v, want ErrStoreClosed", err)
	}
	if _, err := store.Replay(context.Background(), runID, 0); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("Replay after Close error = %v, want ErrStoreClosed", err)
	}
	if _, err := store.Subscribe(context.Background(), runID, 0); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("Subscribe after Close error = %v, want ErrStoreClosed", err)
	}
	if err := store.Verify(runID); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("Verify after Close error = %v, want ErrStoreClosed", err)
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

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %#o, want %#o", path, got, want)
	}
}
