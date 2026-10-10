package artifact

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func TestFileStoreRoundTripsCanonicalArtifactWithinTenant(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	value, err := New(KindProblem, domain.ProblemSchemaVersion, map[string]any{
		"z": int64(1),
		"a": "value",
	})
	if err != nil {
		t.Fatal(err)
	}

	ref, err := store.Put(t.Context(), "tenant-a", value)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Digest != value.Digest() || ref.Kind != KindProblem ||
		ref.SchemaVersion != domain.ProblemSchemaVersion {
		t.Fatalf("artifact ref = %+v", ref)
	}

	reader, metadata, err := store.Open(t.Context(), "tenant-a", ref.Digest)
	if err != nil {
		t.Fatal(err)
	}
	raw, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read artifact: read=%v close=%v", readErr, closeErr)
	}
	if string(raw) != `{"a":"value","z":1}` {
		t.Fatalf("artifact document = %s", raw)
	}
	if metadata.Digest != ref.Digest || metadata.SizeBytes != int64(len(raw)) {
		t.Fatalf("metadata = %+v", metadata)
	}

	_, _, err = store.Open(t.Context(), "tenant-b", ref.Digest)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant Open error = %v, want ErrNotFound", err)
	}
}

func TestNewFileStoreCreatesNestedRootOneLevelAtATime(t *testing.T) {
	root := filepath.Join(t.TempDir(), "one", "two", "three")
	store, err := NewFileStore(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	value, err := New(KindEvidence, "delivery.evidence.v1", map[string]any{"ok": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(t.Context(), "tenant-a", value); err != nil {
		t.Fatal(err)
	}
	if err := store.Verify(t.Context(), "tenant-a", value.Digest()); err != nil {
		t.Fatal(err)
	}
}

func TestFileStoreConcurrentPutConvergesOnOneArtifact(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	value, err := New(KindPlan, domain.PlanSchemaVersion, map[string]any{"value": int64(42)})
	if err != nil {
		t.Fatal(err)
	}

	const writers = 24
	refs := make(chan ArtifactRef, writers)
	errs := make(chan error, writers)
	var group sync.WaitGroup
	for range writers {
		group.Add(1)
		go func() {
			defer group.Done()
			ref, putErr := store.Put(context.Background(), "tenant-a", value)
			refs <- ref
			errs <- putErr
		}()
	}
	group.Wait()
	close(refs)
	close(errs)

	for putErr := range errs {
		if putErr != nil {
			t.Fatalf("concurrent Put error = %v", putErr)
		}
	}
	for ref := range refs {
		if ref.Digest != value.Digest() {
			t.Fatalf("concurrent Put digest = %q, want %q", ref.Digest, value.Digest())
		}
	}
	if err := store.Verify(t.Context(), "tenant-a", value.Digest()); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestFileStoreRejectsCorruptedContent(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	value, err := New(KindValidationReport, domain.ValidationSchemaVersion, map[string]any{
		"valid": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put(t.Context(), "tenant-a", value)
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.artifactPath("tenant-a", ref.Digest, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"tampered":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := store.Verify(t.Context(), "tenant-a", ref.Digest); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Verify error = %v, want ErrIntegrity", err)
	}
	if _, _, err := store.Open(t.Context(), "tenant-a", ref.Digest); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Open error = %v, want ErrIntegrity", err)
	}
}

func TestFileStoreHonorsCancellationAndSizeLimit(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), 128)
	if err != nil {
		t.Fatal(err)
	}
	large, err := New(KindEvidence, "delivery.evidence.v1", map[string]any{
		"payload": "this payload is intentionally larger than the configured artifact limit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(t.Context(), "tenant-a", large); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("large Put error = %v, want ErrTooLarge", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	small, err := New(KindEvidence, "delivery.evidence.v1", map[string]any{"ok": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, "tenant-a", small); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Put error = %v, want context.Canceled", err)
	}
}
