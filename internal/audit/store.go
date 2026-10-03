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
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/securefs"
)

var (
	ErrRunNotFound  = errors.New("audit run not found")
	ErrCursorAhead  = errors.New("audit cursor is ahead of the run")
	ErrWriterLocked = errors.New("audit directory is already open by another writer")
	ErrStoreClosed  = errors.New("audit store is closed")
)

type runLog struct {
	events      []Event
	eventByID   map[string]Event
	subscribers map[int]chan Event
	nextSubID   int
}

type Store struct {
	clock   func() time.Time
	dirFile *os.File

	mu       sync.Mutex
	runs     map[domain.RunID]*runLog
	failed   error
	closed   bool
	closeErr error
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
	dirFile, err := securefs.OpenDirectory(dir, 0o700)
	if err != nil {
		return nil, fmt.Errorf("secure audit directory: %w", err)
	}
	if err := lockWriter(dirFile); err != nil {
		_ = dirFile.Close()
		return nil, err
	}
	store := &Store{
		clock:   clock,
		dirFile: dirFile,
		runs:    make(map[domain.RunID]*runLog),
	}
	closeOnError := func(err error) (*Store, error) {
		return nil, errors.Join(err, store.Close())
	}
	entries, err := dirFile.ReadDir(-1)
	if err != nil {
		return closeOnError(fmt.Errorf("list audit journals: %w", err))
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "audit-") && strings.HasSuffix(entry.Name(), ".jsonl") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if err := store.load(name); err != nil {
			return closeOnError(err)
		}
	}
	return store, nil
}

func lockWriter(dir *os.File) error {
	if err := syscall.Flock(int(dir.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return ErrWriterLocked
		}
		return fmt.Errorf("lock audit directory: %w", err)
	}
	return nil
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
	if s.closed {
		return Event{}, ErrStoreClosed
	}
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
	if s.closed {
		return nil, ErrStoreClosed
	}
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
	if s.closed {
		s.mu.Unlock()
		return nil, ErrStoreClosed
	}
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
	if s.closed {
		return ErrStoreClosed
	}
	run, ok := s.runs[runID]
	if !ok {
		return ErrRunNotFound
	}
	return verifyEvents(runID, run.events)
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	for _, run := range s.runs {
		for id, subscriber := range run.subscribers {
			close(subscriber)
			delete(run.subscribers, id)
		}
	}
	dirFile := s.dirFile
	s.dirFile = nil

	if dirFile != nil {
		unlockErr := syscall.Flock(int(dirFile.Fd()), syscall.LOCK_UN)
		fileCloseErr := dirFile.Close()
		if unlockErr != nil {
			unlockErr = fmt.Errorf("unlock audit directory: %w", unlockErr)
		}
		if fileCloseErr != nil {
			fileCloseErr = fmt.Errorf("close audit directory lock: %w", fileCloseErr)
		}
		s.closeErr = errors.Join(unlockErr, fileCloseErr)
	}
	s.closed = true
	return s.closeErr
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
	name, err := journalFileName(event.RunID)
	if err != nil {
		return err
	}
	file, isNew, err := securefs.OpenOrCreateRegularAt(
		s.dirFile,
		name,
		os.O_APPEND|os.O_WRONLY,
		0o600,
	)
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
		if err := s.dirFile.Sync(); err != nil {
			return fmt.Errorf("sync audit directory: %w", err)
		}
	}
	return nil
}

func (s *Store) load(name string) error {
	runID := domain.RunID(strings.TrimSuffix(strings.TrimPrefix(name, "audit-"), ".jsonl"))
	if runID == "" {
		return fmt.Errorf("invalid audit journal name %q", name)
	}
	file, err := securefs.OpenExistingRegularAt(s.dirFile, name, os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open audit journal %q: %w", name, err)
	}
	defer file.Close()
	raw, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read audit journal %q: %w", name, err)
	}
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		lastNewline := bytes.LastIndexByte(raw, '\n')
		keep := 0
		if lastNewline >= 0 {
			keep = lastNewline + 1
		}
		if err := file.Truncate(int64(keep)); err != nil {
			return fmt.Errorf("truncate incomplete audit tail %q: %w", name, err)
		}
		if err := file.Sync(); err != nil {
			return fmt.Errorf("sync repaired audit journal %q: %w", name, err)
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
			return fmt.Errorf("decode audit journal %q: %w", name, err)
		}
		events = append(events, event)
	}
	if err := verifyEvents(runID, events); err != nil {
		return fmt.Errorf("verify audit journal %q: %w", name, err)
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

func journalFileName(runID domain.RunID) (string, error) {
	for _, char := range runID {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
		case char == '-', char == '_', char == '.':
		default:
			return "", fmt.Errorf("audit run ID contains unsupported characters")
		}
	}
	if runID == "" {
		return "", fmt.Errorf("audit run ID is empty")
	}
	return "audit-" + string(runID) + ".jsonl", nil
}
