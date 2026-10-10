package artifact

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func TestS3StoreRoundTripsAndConvergesWithoutOverwrite(t *testing.T) {
	client := &memoryS3Client{objects: make(map[string][]byte)}
	store, err := NewS3Store(client, "delivery-artifacts", "prod/v1", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	value, err := New(KindPlan, domain.PlanSchemaVersion, map[string]any{
		"plan": "value",
	})
	if err != nil {
		t.Fatal(err)
	}
	const writers = 20
	var wait sync.WaitGroup
	errs := make(chan error, writers)
	for range writers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			ref, putErr := store.Put(context.Background(), "tenant-a", value)
			if putErr == nil && ref.Digest != value.Digest() {
				putErr = io.ErrUnexpectedEOF
			}
			errs <- putErr
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if client.created != 1 {
		t.Fatalf("created objects = %d, want 1", client.created)
	}
	reader, metadata, err := store.Open(t.Context(), "tenant-a", value.Digest())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"plan":"value"}` ||
		metadata.Kind != KindPlan ||
		metadata.SchemaVersion != domain.PlanSchemaVersion {
		t.Fatalf("artifact = %s metadata=%+v", raw, metadata)
	}
	if _, _, err := store.Open(
		t.Context(),
		"tenant-b",
		value.Digest(),
	); err != ErrNotFound {
		t.Fatalf("cross-tenant Open error = %v, want ErrNotFound", err)
	}
}

func TestS3StoreRejectsCorruptionAndUnsafeConfiguration(t *testing.T) {
	client := &memoryS3Client{objects: make(map[string][]byte)}
	if _, err := NewS3Store(client, "bucket", "../unsafe", 1<<20); err == nil {
		t.Fatal("NewS3Store accepted parent traversal")
	}
	store, err := NewS3Store(client, "bucket", "", 1<<20)
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
	key, err := store.objectKey("tenant-a", value.Digest())
	if err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	client.objects[key] = []byte(`{"tampered":true}`)
	client.mu.Unlock()
	if err := store.Verify(
		t.Context(),
		"tenant-a",
		value.Digest(),
	); err != ErrIntegrity && !strings.Contains(err.Error(), ErrIntegrity.Error()) {
		t.Fatalf("Verify error = %v, want ErrIntegrity", err)
	}
}

type memoryS3Client struct {
	mu      sync.Mutex
	objects map[string][]byte
	created int
}

func (client *memoryS3Client) PutObject(
	_ context.Context,
	input *s3.PutObjectInput,
	_ ...func(*s3.Options),
) (*s3.PutObjectOutput, error) {
	raw, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if _, exists := client.objects[*input.Key]; exists {
		return nil, &smithy.GenericAPIError{
			Code:    "PreconditionFailed",
			Message: "already exists",
		}
	}
	client.objects[*input.Key] = append([]byte(nil), raw...)
	client.created++
	return &s3.PutObjectOutput{}, nil
}

func (client *memoryS3Client) GetObject(
	_ context.Context,
	input *s3.GetObjectInput,
	_ ...func(*s3.Options),
) (*s3.GetObjectOutput, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	raw, exists := client.objects[*input.Key]
	if !exists {
		return nil, &smithy.GenericAPIError{
			Code:    "NoSuchKey",
			Message: "missing",
		}
	}
	return &s3.GetObjectOutput{
		Body: io.NopCloser(strings.NewReader(string(raw))),
	}, nil
}

var _ S3API = (*memoryS3Client)(nil)
