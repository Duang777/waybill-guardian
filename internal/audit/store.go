package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
)

var (
	ErrRunNotFound = errors.New("audit run not found")
	ErrCursorAhead = errors.New("audit cursor is ahead of the run")
)

type runLog struct {
	events      []Event
	eventByID   map[string]Event
	subscribers map[int]chan Event
	nextSubID   int
}

type Store struct {
	dir   string
	clock func() time.Time

	mu     sync.Mutex
	runs   map[domain.RunID]*runLog
	failed error
}

type Subscription struct {
	events <-chan Event
	close  func()
	once   sync.Once
}

func (s *Subscription) Events() <-chan Event {
	return s.events
}

func (s *Subscription) Close() {
	if s == nil || s.close == nil {
		return
	}
	s.once.Do(s.close)
}

func Open(dir string, clock func() time.Time) (*Store, error) {
	if clock == nil {
		clock = time.Now
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create audit directory: %w", err)
	}
	store := &Store{dir: dir, clock: clock, runs: make(map[domain.RunID]*runLog)}
	paths, err := filepath.Glob(filepath.Join(dir, "audit-*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("list audit journals: %w", err)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := store.load(path); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (s *Store) Append(ctx context.Context, runID domain.RunID, draft Draft) (Event, error) {
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	if runID == "" || draft.EventID == "" || draft.Type == "" {
		return Event{}, fmt.Errorf("run_id, event_id, and type are required")
	}
	payload, err := Redact(draft.Payload)
	if err != nil {
		return Event{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil {
		return Event{}, s.failed
	}
	run := s.ensureRun(runID)
	if existing, ok := run.eventByID[draft.EventID]; ok {
		return existing, nil
	}
	prevHash := GenesisHash
	if len(run.events) > 0 {
		prevHash = run.events[len(run.events)-1].Hash
	}
	event := Event{
		SchemaVersion: 1,
		EventID:       draft.EventID,
		Seq:           Seq(len(run.events) + 1),
		TS:            s.clock().UTC(),
		RunID:         runID,
		Actor:         draft.Actor,
		Type:          draft.Type,
		Payload:       payload,
		PrevHash:      prevHash,
	}
	event.Hash, err = hashEvent(event)
	if err != nil {
		return Event{}, err
	}
	if err := s.appendLocked(event); err != nil {
		s.failed = fmt.Errorf("audit store requires restart after append failure: %w", err)
		return Event{}, s.failed
	}
	run.events = append(run.events, event)
	run.eventByID[event.EventID] = event
	for id, subscriber := range run.subscribers {
		select {
		case subscriber <- event:
		default:
			close(subscriber)
			delete(run.subscribers, id)
		}
	}
	return event, nil
}

func (s *Store) Replay(ctx context.Context, runID domain.RunID, after Seq) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[runID]
	if !ok || len(run.events) == 0 {
		return nil, ErrRunNotFound
	}
	if after > Seq(len(run.events)) {
		return nil, ErrCursorAhead
	}
	return append([]Event(nil), run.events[int(after):]...), nil
}

func (s *Store) Subscribe(ctx context.Context, runID domain.RunID, after Seq) (*Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	run, ok := s.runs[runID]
	if !ok || len(run.events) == 0 {
		s.mu.Unlock()
		return nil, ErrRunNotFound
	}
	if after > Seq(len(run.events)) {
		s.mu.Unlock()
		return nil, ErrCursorAhead
	}
	backlog := append([]Event(nil), run.events[int(after):]...)
	size := len(backlog) + 64
	if size < 64 {
		size = 64
	}
	ch := make(chan Event, size)
	for _, event := range backlog {
		ch <- event
	}
	id := run.nextSubID
	run.nextSubID++
	run.subscribers[id] = ch
	s.mu.Unlock()

	subscription := &Subscription{
		events: ch,
		close: func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			run, ok := s.runs[runID]
			if !ok {
				return
			}
			if current, exists := run.subscribers[id]; exists {
				close(current)
				delete(run.subscribers, id)
			}
		},
	}
	go func() {
		<-ctx.Done()
		subscription.Close()
	}()
	return subscription, nil
}

func (s *Store) AllEvents() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []Event
	for _, run := range s.runs {
		result = append(result, run.events...)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].RunID == result[j].RunID {
			return result[i].Seq < result[j].Seq
		}
		return result[i].RunID < result[j].RunID
	})
	return result
}

func (s *Store) Verify(runID domain.RunID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[runID]
	if !ok {
		return ErrRunNotFound
	}
	return verifyEvents(runID, run.events)
}

func (s *Store) ensureRun(runID domain.RunID) *runLog {
	run := s.runs[runID]
	if run == nil {
		run = &runLog{
			eventByID:   make(map[string]Event),
			subscribers: make(map[int]chan Event),
		}
		s.runs[runID] = run
	}
	return run
}

func (s *Store) appendLocked(event Event) error {
	path := s.path(event.RunID)
	_, statErr := os.Stat(path)
	isNew := errors.Is(statErr, os.ErrNotExist)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open audit journal: %w", err)
	}
	line, err := json.Marshal(event)
	if err == nil {
		line = append(line, '\n')
		_, err = file.Write(line)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return fmt.Errorf("append audit event: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close audit journal: %w", closeErr)
	}
	if isNew {
		if err := syncDirectory(s.dir); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) load(path string) error {
	base := filepath.Base(path)
	runID := domain.RunID(strings.TrimSuffix(strings.TrimPrefix(base, "audit-"), ".jsonl"))
	if runID == "" {
		return fmt.Errorf("invalid audit journal name %q", base)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read audit journal %q: %w", path, err)
	}
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		lastNewline := bytes.LastIndexByte(raw, '\n')
		keep := 0
		if lastNewline >= 0 {
			keep = lastNewline + 1
		}
		if err := os.Truncate(path, int64(keep)); err != nil {
			return fmt.Errorf("truncate incomplete audit tail %q: %w", path, err)
		}
		raw = raw[:keep]
	}
	var events []Event
	decoder := json.NewDecoder(bytes.NewReader(raw))
	for {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("decode audit journal %q: %w", path, err)
		}
		events = append(events, event)
	}
	if err := verifyEvents(runID, events); err != nil {
		return fmt.Errorf("verify audit journal %q: %w", path, err)
	}
	run := s.ensureRun(runID)
	for _, event := range events {
		if _, duplicate := run.eventByID[event.EventID]; duplicate {
			return fmt.Errorf("duplicate event_id %q", event.EventID)
		}
		run.events = append(run.events, event)
		run.eventByID[event.EventID] = event
	}
	return nil
}

func verifyEvents(runID domain.RunID, events []Event) error {
	prevHash := GenesisHash
	for index, event := range events {
		expectedSeq := Seq(index + 1)
		if event.RunID != runID {
			return fmt.Errorf("event %q belongs to run %q", event.EventID, event.RunID)
		}
		if event.Seq != expectedSeq {
			return fmt.Errorf("event %q has seq %d, want %d", event.EventID, event.Seq, expectedSeq)
		}
		if event.PrevHash != prevHash {
			return fmt.Errorf("event %q has invalid prev_hash", event.EventID)
		}
		hash, err := hashEvent(event)
		if err != nil {
			return err
		}
		if event.Hash != hash {
			return fmt.Errorf("event %q has invalid hash", event.EventID)
		}
		prevHash = event.Hash
	}
	return nil
}

func hashEvent(event Event) (string, error) {
	material := struct {
		SchemaVersion int             `json:"schema_version"`
		EventID       string          `json:"event_id"`
		Seq           Seq             `json:"seq"`
		TS            time.Time       `json:"ts"`
		RunID         domain.RunID    `json:"run_id"`
		Actor         Actor           `json:"actor"`
		Type          EventType       `json:"type"`
		Payload       json.RawMessage `json:"payload"`
		PrevHash      string          `json:"prev_hash"`
	}{
		SchemaVersion: event.SchemaVersion,
		EventID:       event.EventID,
		Seq:           event.Seq,
		TS:            event.TS.UTC(),
		RunID:         event.RunID,
		Actor:         event.Actor,
		Type:          event.Type,
		Payload:       event.Payload,
		PrevHash:      event.PrevHash,
	}
	raw, err := json.Marshal(material)
	if err != nil {
		return "", fmt.Errorf("marshal audit hash material: %w", err)
	}
	sum := sha256.Sum256(append([]byte("waybill-audit-v1\n"), raw...))
	return hex.EncodeToString(sum[:]), nil
}

func (s *Store) path(runID domain.RunID) string {
	return filepath.Join(s.dir, "audit-"+string(runID)+".jsonl")
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open audit directory: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync audit directory: %w", err)
	}
	return nil
}
