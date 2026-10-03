package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Duang777/waybill-guardian/internal/securefs"
	"github.com/hastekit/agent-sdk-go/pkg/agents/history"
)

const defaultHistoryRetention = 7 * 24 * time.Hour

type HistoryPolicy struct {
	Retention time.Duration
	Clock     func() time.Time
}

type historyMetadata struct {
	SchemaVersion  uint16     `json:"schema_version"`
	ConversationID string     `json:"conversation_id"`
	ThreadID       string     `json:"thread_id"`
	TerminalAt     *time.Time `json:"terminal_at,omitempty"`
}

type secureHistoryPersistence struct {
	*history.FileConversationPersistence
	dir   string
	clock func() time.Time
	mu    sync.Mutex
}

func normalizeHistoryPolicy(policy HistoryPolicy) (HistoryPolicy, error) {
	if policy.Retention == 0 {
		policy.Retention = defaultHistoryRetention
	}
	if policy.Retention < 0 {
		return HistoryPolicy{}, fmt.Errorf("history retention must be positive")
	}
	if policy.Clock == nil {
		policy.Clock = time.Now
	}
	return policy, nil
}

func openSecureHistory(
	dir string,
	policy HistoryPolicy,
) (*history.CommonConversationManager, error) {
	policy, err := normalizeHistoryPolicy(policy)
	if err != nil {
		return nil, err
	}
	dirFile, err := securefs.OpenDirectory(dir, 0o700)
	if err != nil {
		return nil, fmt.Errorf("secure hastekit history directory: %w", err)
	}
	if err := secureFiles(dir); err != nil {
		_ = dirFile.Close()
		return nil, err
	}
	if err := cleanHistoryFiles(dir, policy.Retention, policy.Clock().UTC()); err != nil {
		_ = dirFile.Close()
		return nil, err
	}
	persistence, err := history.NewFileConversationPersistence(dir)
	if err != nil {
		_ = dirFile.Close()
		return nil, err
	}
	if err := dirFile.Close(); err != nil {
		return nil, errors.Join(
			fmt.Errorf("close hastekit history directory: %w", err),
			persistence.Close(),
		)
	}
	secure := &secureHistoryPersistence{
		FileConversationPersistence: persistence,
		dir:                         dir,
		clock:                       policy.Clock,
	}
	return history.NewConversationManager(guardHistoryPersistence(secure)), nil
}

func (p *secureHistoryPersistence) SaveMessages(
	ctx context.Context,
	namespace string,
	groupID string,
	runID string,
	previousRunID string,
	threadID string,
	conversationID string,
	messages []history.Message,
	meta map[string]any,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	metadata, err := ensureHistoryMetadata(p.dir, conversationID, threadID)
	if err != nil {
		return err
	}
	if err := prepareHistoryFile(p.dir, conversationID); err != nil {
		return err
	}
	if err := p.FileConversationPersistence.SaveMessages(
		ctx,
		namespace,
		groupID,
		runID,
		previousRunID,
		threadID,
		conversationID,
		messages,
		meta,
	); err != nil {
		return err
	}
	if historyIsTerminal(meta) && metadata.TerminalAt == nil {
		terminalAt := p.clock().UTC()
		metadata.TerminalAt = &terminalAt
		if err := writeHistoryMetadata(p.dir, metadata); err != nil {
			return err
		}
	}
	return nil
}

func (p *secureHistoryPersistence) SaveSummary(
	ctx context.Context,
	namespace string,
	summary history.Summary,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	conversationID := summary.ThreadID
	messages, err := p.FileConversationPersistence.LoadMessages(ctx, namespace, summary.ThreadID, "")
	if err != nil {
		return err
	}
	if len(messages) > 0 && messages[0].ConversationID != "" {
		conversationID = messages[0].ConversationID
	}
	if _, err := ensureHistoryMetadata(p.dir, conversationID, summary.ThreadID); err != nil {
		return err
	}
	if err := prepareHistoryFile(p.dir, conversationID); err != nil {
		return err
	}
	return p.FileConversationPersistence.SaveSummary(ctx, namespace, summary)
}

func prepareHistoryFile(dir, conversationID string) error {
	if !isSafeHistoryID(conversationID) {
		return fmt.Errorf("hastekit history ID contains unsupported characters")
	}
	path := filepath.Join(dir, conversationID+".jsonl")
	file, _, err := securefs.OpenOrCreateRegular(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("prepare hastekit history file: %w", err)
	}
	closeErr := file.Close()
	if closeErr != nil {
		return fmt.Errorf("close hastekit history file %q: %w", path, closeErr)
	}
	return nil
}

func isSafeHistoryID(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
		case char == '-', char == '_', char == '.':
		default:
			return false
		}
	}
	return true
}

func secureFiles(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read hastekit history directory: %w", err)
	}
	for _, entry := range entries {
		if !isHistoryFile(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, err := securefs.OpenExistingRegular(path, os.O_RDWR, 0o600)
		if err != nil {
			return fmt.Errorf("secure hastekit history file %q: %w", path, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close hastekit history file %q: %w", path, err)
		}
	}
	return nil
}

func isHistoryFile(name string) bool {
	return strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".meta.json")
}

func historyMetadataPath(dir, conversationID string) string {
	return filepath.Join(dir, conversationID+".meta.json")
}

func ensureHistoryMetadata(
	dir string,
	conversationID string,
	threadID string,
) (historyMetadata, error) {
	if !isSafeHistoryID(conversationID) || !isSafeHistoryID(threadID) {
		return historyMetadata{}, fmt.Errorf("hastekit history ID contains unsupported characters")
	}
	metadata, err := readHistoryMetadata(dir, conversationID)
	switch {
	case err == nil:
		if metadata.ConversationID != conversationID || metadata.ThreadID != threadID {
			return historyMetadata{}, fmt.Errorf("hastekit history metadata identity mismatch")
		}
		return metadata, nil
	case !errors.Is(err, os.ErrNotExist):
		return historyMetadata{}, err
	}
	metadata = historyMetadata{
		SchemaVersion:  uint16(historySchemaVersion),
		ConversationID: conversationID,
		ThreadID:       threadID,
	}
	if err := writeHistoryMetadata(dir, metadata); err != nil {
		return historyMetadata{}, err
	}
	return metadata, nil
}

func readHistoryMetadata(dir, conversationID string) (historyMetadata, error) {
	path := historyMetadataPath(dir, conversationID)
	file, err := securefs.OpenExistingRegular(path, os.O_RDONLY, 0o600)
	if err != nil {
		return historyMetadata{}, err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var metadata historyMetadata
	if err := decoder.Decode(&metadata); err != nil {
		return historyMetadata{}, fmt.Errorf("decode hastekit history metadata %q: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return historyMetadata{}, fmt.Errorf("decode hastekit history metadata %q: %w", path, err)
	}
	if metadata.SchemaVersion != uint16(historySchemaVersion) {
		return historyMetadata{}, fmt.Errorf(
			"hastekit history metadata %q has unsupported schema version",
			path,
		)
	}
	if !isSafeHistoryID(metadata.ConversationID) || !isSafeHistoryID(metadata.ThreadID) {
		return historyMetadata{}, fmt.Errorf("hastekit history metadata %q has invalid identity", path)
	}
	if metadata.TerminalAt != nil {
		terminalAt := metadata.TerminalAt.UTC()
		metadata.TerminalAt = &terminalAt
	}
	return metadata, nil
}

func writeHistoryMetadata(dir string, metadata historyMetadata) error {
	dirFile, err := securefs.OpenDirectory(dir, 0o700)
	if err != nil {
		return fmt.Errorf("open hastekit history directory: %w", err)
	}
	defer dirFile.Close()

	name := metadata.ConversationID + ".meta.json"
	file, _, err := securefs.OpenOrCreateRegularAt(dirFile, name, os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open hastekit history metadata %q: %w", name, err)
	}
	defer file.Close()
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("truncate hastekit history metadata %q: %w", name, err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek hastekit history metadata %q: %w", name, err)
	}
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(metadata); err != nil {
		return fmt.Errorf("encode hastekit history metadata %q: %w", name, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync hastekit history metadata %q: %w", name, err)
	}
	return nil
}

func historyIsTerminal(meta map[string]any) bool {
	runState, ok := meta["run_state"].(map[string]any)
	if !ok {
		return false
	}
	status := fmt.Sprint(runState["status"])
	return status == "completed" || status == "error"
}

func cleanHistoryFiles(dir string, retention time.Duration, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read hastekit history directory: %w", err)
	}
	jsonlIDs := make(map[string]struct{})
	metadataIDs := make(map[string]struct{})
	for _, entry := range entries {
		switch {
		case strings.HasSuffix(entry.Name(), ".jsonl"):
			jsonlIDs[strings.TrimSuffix(entry.Name(), ".jsonl")] = struct{}{}
		case strings.HasSuffix(entry.Name(), ".meta.json"):
			metadataIDs[strings.TrimSuffix(entry.Name(), ".meta.json")] = struct{}{}
		}
	}
	cutoff := now.Add(-retention)
	for conversationID := range jsonlIDs {
		metadata, err := readHistoryMetadata(dir, conversationID)
		if err != nil ||
			metadata.ConversationID != conversationID ||
			(metadata.TerminalAt != nil && metadata.TerminalAt.Before(cutoff)) {
			if err := removeHistoryConversation(dir, conversationID); err != nil {
				return err
			}
			continue
		}
		delete(metadataIDs, conversationID)
	}
	for conversationID := range metadataIDs {
		if err := removeHistoryFile(historyMetadataPath(dir, conversationID)); err != nil {
			return err
		}
	}
	return nil
}

func removeHistoryConversation(dir, conversationID string) error {
	for _, path := range []string{
		filepath.Join(dir, conversationID+".jsonl"),
		historyMetadataPath(dir, conversationID),
	} {
		if err := removeHistoryFile(path); err != nil {
			return err
		}
	}
	return nil
}

func removeHistoryFile(path string) error {
	file, err := securefs.OpenExistingRegular(path, os.O_RDONLY, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("secure history cleanup file %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close history cleanup file %q: %w", path, err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove history cleanup file %q: %w", path, err)
	}
	return nil
}
