package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Duang777/waybill-guardian/internal/securefs"
	"github.com/hastekit/agent-sdk-go/pkg/agents/history"
)

type secureHistoryPersistence struct {
	*history.FileConversationPersistence
	dir string
}

func openSecureHistory(dir string) (*history.CommonConversationManager, error) {
	dirFile, err := securefs.OpenDirectory(dir, 0o700)
	if err != nil {
		return nil, fmt.Errorf("secure hastekit history directory: %w", err)
	}
	if err := secureFiles(dir); err != nil {
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
	if err := prepareHistoryFile(p.dir, conversationID); err != nil {
		return err
	}
	return p.FileConversationPersistence.SaveMessages(
		ctx,
		namespace,
		groupID,
		runID,
		previousRunID,
		threadID,
		conversationID,
		messages,
		meta,
	)
}

func (p *secureHistoryPersistence) SaveSummary(
	ctx context.Context,
	namespace string,
	summary history.Summary,
) error {
	conversationID := summary.ThreadID
	messages, err := p.FileConversationPersistence.LoadMessages(ctx, namespace, summary.ThreadID, "")
	if err != nil {
		return err
	}
	if len(messages) > 0 && messages[0].ConversationID != "" {
		conversationID = messages[0].ConversationID
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
		if !strings.HasSuffix(entry.Name(), ".jsonl") {
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
