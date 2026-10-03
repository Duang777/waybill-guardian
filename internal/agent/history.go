package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hastekit/agent-sdk-go/pkg/agents/history"
)

type secureHistoryPersistence struct {
	*history.FileConversationPersistence
	dir string
}

func openSecureHistory(dir string) (*history.CommonConversationManager, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create hastekit history directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secure hastekit history directory: %w", err)
	}
	persistence, err := history.NewFileConversationPersistence(dir)
	if err != nil {
		return nil, err
	}
	secure := &secureHistoryPersistence{
		FileConversationPersistence: persistence,
		dir:                         dir,
	}
	if err := secureFiles(dir); err != nil {
		return nil, errors.Join(err, persistence.Close())
	}
	return history.NewConversationManager(secure), nil
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
	return secureFiles(p.dir)
}

func (p *secureHistoryPersistence) SaveSummary(
	ctx context.Context,
	namespace string,
	summary history.Summary,
) error {
	if err := p.FileConversationPersistence.SaveSummary(ctx, namespace, summary); err != nil {
		return err
	}
	return secureFiles(p.dir)
}

func secureFiles(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read hastekit history directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("secure hastekit history file %q: %w", path, err)
		}
	}
	return nil
}
