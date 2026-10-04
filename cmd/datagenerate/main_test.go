package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Duang777/waybill-guardian/internal/platform/filestore"
)

func TestRunGeneratesValidatedNetworkDataset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "simulated.json")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(
		[]string{"--output", path, "--waybills", "200"},
		&stdout,
		&stderr,
	); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	loaded, err := filestore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Stats.Waybills != 200 ||
		loaded.Stats.Anomalies != 67 ||
		loaded.Stats.Hubs != 72 ||
		loaded.Stats.Vehicles != 200 ||
		loaded.Stats.Routes != 72 {
		t.Fatalf("stats = %+v", loaded.Stats)
	}
	if stdout.String() != "generated dataset=simulated-network-v1 hubs=72 routes=72 waybills=200 output=simulated.json\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	generated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile(
		filepath.Join("..", "..", "data", "simulated", "waybills-v1.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, committed) {
		t.Fatal("committed simulated dataset does not match the generator")
	}
}

func TestRunRejectsTooFewWaybills(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(
		[]string{"--output", "ignored.json", "--waybills", "71"},
		&stdout,
		&stderr,
	); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
}
