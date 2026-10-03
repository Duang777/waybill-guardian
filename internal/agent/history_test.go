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

func TestSecureHistoryRejectsLinkedConversationFiles(t *testing.T) {
	tests := []struct {
		name string
		link func(string, string) error
	}{
		{name: "symbolic link", link: os.Symlink},
		{name: "hard link", link: os.Link},
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
			if err := test.link(target, filepath.Join(dir, "linked.jsonl")); err != nil {
				t.Fatal(err)
			}

			manager, err := openSecureHistory(dir)
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
