package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	writeHistoryMetadataFixture(t, dir, historyMetadata{
		SchemaVersion:  uint16(historySchemaVersion),
		ConversationID: "existing",
		ThreadID:       "thread-existing",
	})

	manager, err := openSecureHistory(dir, HistoryPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	assertFileMode(t, dir, 0o700)
	assertFileMode(t, existing, 0o600)
	assertFileMode(t, filepath.Join(dir, "existing.meta.json"), 0o600)

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
	assertFileMode(t, filepath.Join(dir, "conversation-1.meta.json"), 0o600)

	if err := manager.ConversationPersistenceAdapter.SaveSummary(
		context.Background(),
		Namespace,
		history.Summary{ID: "summary-1", ThreadID: "thread-1"},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "thread-1.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("summary created a second conversation file: %v", err)
	}
}

func TestSecureHistoryRejectsLinkedConversationFiles(t *testing.T) {
	tests := []struct {
		name     string
		fileName string
		link     func(string, string) error
	}{
		{name: "symbolic JSONL link", fileName: "linked.jsonl", link: os.Symlink},
		{name: "hard JSONL link", fileName: "linked.jsonl", link: os.Link},
		{name: "symbolic metadata link", fileName: "linked.meta.json", link: os.Symlink},
		{name: "hard metadata link", fileName: "linked.meta.json", link: os.Link},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "history")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "target")
			if err := os.WriteFile(target, []byte("do not touch"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := test.link(target, filepath.Join(dir, test.fileName)); err != nil {
				t.Fatal(err)
			}

			manager, err := openSecureHistory(dir, HistoryPolicy{})
			if manager != nil {
				_ = manager.Close()
				t.Fatal("openSecureHistory accepted a linked conversation file")
			}
			if err == nil {
				t.Fatal("openSecureHistory returned no error")
			}
			assertFileMode(t, target, 0o644)
			raw, readErr := os.ReadFile(target)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(raw) != "do not touch" {
				t.Fatalf("linked target was modified: %q", raw)
			}
		})
	}
}

func TestSecureHistoryRemovesLegacyFilesBeforeReplay(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, "legacy.jsonl")
	if err := os.WriteFile(legacy, []byte(`{"legacy_phone":"13800138000"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	manager, err := openSecureHistory(dir, HistoryPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy history still exists: %v", err)
	}
}

func TestSecureHistoryPrunesOnlyExpiredTerminalConversations(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	retention := 24 * time.Hour
	dir := filepath.Join(t.TempDir(), "history")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	expiredAt := now.Add(-retention - time.Second)
	recentAt := now.Add(-retention + time.Second)
	for _, fixture := range []struct {
		id         string
		terminalAt *time.Time
	}{
		{id: "active-old"},
		{id: "terminal-expired", terminalAt: &expiredAt},
		{id: "terminal-recent", terminalAt: &recentAt},
	} {
		if err := os.WriteFile(filepath.Join(dir, fixture.id+".jsonl"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		writeHistoryMetadataFixture(t, dir, historyMetadata{
			SchemaVersion:  uint16(historySchemaVersion),
			ConversationID: fixture.id,
			ThreadID:       "thread-" + fixture.id,
			TerminalAt:     fixture.terminalAt,
		})
		old := now.Add(-30 * 24 * time.Hour)
		if err := os.Chtimes(filepath.Join(dir, fixture.id+".jsonl"), old, old); err != nil {
			t.Fatal(err)
		}
	}

	manager, err := openSecureHistory(dir, HistoryPolicy{
		Retention: retention,
		Clock:     func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	assertHistoryConversationExists(t, dir, "active-old", true)
	assertHistoryConversationExists(t, dir, "terminal-expired", false)
	assertHistoryConversationExists(t, dir, "terminal-recent", true)
}

func TestSecureHistoryMarksTerminalConversation(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	dir := filepath.Join(t.TempDir(), "history")
	manager, err := openSecureHistory(dir, HistoryPolicy{
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	if err := manager.ConversationPersistenceAdapter.SaveMessages(
		context.Background(),
		Namespace,
		history.DefaultGroupID,
		"run-terminal",
		"",
		"thread-terminal",
		"conversation-terminal",
		nil,
		map[string]any{"run_state": map[string]any{"status": "completed"}},
	); err != nil {
		t.Fatal(err)
	}
	metadata, err := readHistoryMetadata(dir, "conversation-terminal")
	if err != nil {
		t.Fatal(err)
	}
	if metadata.TerminalAt == nil || !metadata.TerminalAt.Equal(now) {
		t.Fatalf("terminal_at = %v, want %s", metadata.TerminalAt, now)
	}
}

func writeHistoryMetadataFixture(t *testing.T, dir string, metadata historyMetadata) {
	t.Helper()
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		historyMetadataPath(dir, metadata.ConversationID),
		append(raw, '\n'),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
}

func assertHistoryConversationExists(t *testing.T, dir, conversationID string, want bool) {
	t.Helper()
	for _, suffix := range []string{".jsonl", ".meta.json"} {
		_, err := os.Stat(filepath.Join(dir, conversationID+suffix))
		if want && err != nil {
			t.Fatalf("%s%s missing: %v", conversationID, suffix, err)
		}
		if !want && !os.IsNotExist(err) {
			t.Fatalf("%s%s still exists: %v", conversationID, suffix, err)
		}
	}
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
