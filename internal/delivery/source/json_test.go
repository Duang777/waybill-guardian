package source

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func TestJSONProviderBuildsSnapshotAndReturnsIndependentValues(t *testing.T) {
	document := sourceTestDocument(t)
	path := writeJSONDocument(t, document)
	provider, err := OpenJSON(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	coordinator := newTestCoordinator(t, provider)

	result, err := coordinator.Build(
		context.Background(),
		sourceTestBuildRequest(document.Manifest),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Problem.ProblemDigest == "" {
		t.Fatal("problem digest is empty")
	}

	snapshot, err := provider.OpenSnapshot(
		context.Background(),
		document.Manifest.TenantID,
		document.Manifest.Ref,
	)
	if err != nil {
		t.Fatal(err)
	}
	revision := revisionFor(document.Manifest, KindFleet)
	first, _, err := snapshot.ReadFleet(context.Background(), revision)
	if err != nil {
		t.Fatal(err)
	}
	first.Vehicles[0].ID = "mutated"
	second, _, err := snapshot.ReadFleet(context.Background(), revision)
	if err != nil {
		t.Fatal(err)
	}
	if second.Vehicles[0].ID != "vehicle-1" {
		t.Fatalf("JSON provider returned shared data: %+v", second.Vehicles[0])
	}
}

func TestJSONProviderReturnsNotFoundAcrossTenantOrRef(t *testing.T) {
	document := sourceTestDocument(t)
	provider, err := OpenJSON(writeJSONDocument(t, document), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		tenant string
		ref    SnapshotRef
	}{
		{tenant: "tenant-b", ref: document.Manifest.Ref},
		{tenant: string(document.Manifest.TenantID), ref: "other-ref"},
	}
	for _, test := range tests {
		_, err := provider.OpenSnapshot(
			context.Background(),
			domainTenant(test.tenant),
			test.ref,
		)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("OpenSnapshot(%q, %q) error = %v, want ErrNotFound",
				test.tenant, test.ref, err)
		}
	}
}

func TestOpenJSONRejectsUnsafeOrMalformedInput(t *testing.T) {
	document := sourceTestDocument(t)
	valid, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	tampered := document
	tampered.Fleet.Data.Vehicles[0].FixedCostCents++
	tamperedRaw, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{
			name: "unknown field",
			raw:  append([]byte(`{"unexpected":true,`), valid[1:]...),
			want: "unknown field",
		},
		{
			name: "duplicate field",
			raw: append(
				[]byte(`{"schema_version":"delivery.source-json.v1",`),
				valid[1:]...,
			),
			want: "duplicate field",
		},
		{
			name: "trailing value",
			raw:  append(valid, []byte(` {}`)...),
			want: "trailing value",
		},
		{
			name: "invalid utf8",
			raw:  []byte{0xff},
			want: "UTF-8",
		},
		{
			name: "content tamper",
			raw:  tamperedRaw,
			want: "content digest",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "snapshot.json")
			if err := os.WriteFile(path, test.raw, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := OpenJSON(path, 1<<20)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("OpenJSON error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestOpenJSONRejectsOversizedAndLinkedFiles(t *testing.T) {
	document := sourceTestDocument(t)
	path := writeJSONDocument(t, document)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJSON(path, info.Size()-1); !errors.Is(err, ErrSourceTooLarge) {
		t.Fatalf("oversized OpenJSON error = %v, want ErrSourceTooLarge", err)
	}

	symlink := filepath.Join(t.TempDir(), "snapshot-link.json")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJSON(symlink, 1<<20); err == nil {
		t.Fatal("OpenJSON accepted a symbolic link")
	}

	hardlink := filepath.Join(t.TempDir(), "snapshot-hardlink.json")
	if err := os.Link(path, hardlink); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJSON(hardlink, 1<<20); err == nil {
		t.Fatal("OpenJSON accepted a multiply-linked file")
	}
}

func writeJSONDocument(t *testing.T, document JSONDocument) string {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func domainTenant(value string) domain.TenantID {
	return domain.TenantID(value)
}
