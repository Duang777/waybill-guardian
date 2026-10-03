package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hastekit/agent-sdk-go/pkg/agents/history"
)

func TestSecureHistoryRestrictsDirectoryAndFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(dir, "existing.jsonl")
	if err := os.WriteFile(existing, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	manager, err := openSecureHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	assertFileMode(t, dir, 0o700)
	assertFileMode(t, existing, 0o600)

	if err := manager.ConversationPersistenceAdapter.SaveMessages(
		context.Background(),
		Namespace,
		history.DefaultGroupID,
		"run-1",
		"",
		"thread-1",
		"conversation-1",
		nil,
		map[string]any{"state": "paused"},
	); err != nil {
		t.Fatal(err)
	}
	assertFileMode(t, filepath.Join(dir, "conversation-1.jsonl"), 0o600)
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %#o, want %#o", path, got, want)
	}
}
