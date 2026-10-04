package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunValidatesTemplate(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(
		[]string{
			"validate",
			"--data",
			filepath.Join("..", "..", "data", "templates", "waybills-v1.csv"),
		},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	const want = "valid dataset=template-v1 format=csv waybills=1 anomalies=1 hubs=0 vehicles=0 routes=0\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunReportsValidationFailureWithoutAbsolutePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.csv")
	if err := os.WriteFile(path, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(
		[]string{"validate", "--data", path},
		&stdout,
		&stderr,
	)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if strings.Contains(stderr.String(), filepath.Dir(path)) {
		t.Fatalf("stderr leaks absolute path: %q", stderr.String())
	}
}

func TestRunRejectsInvalidInvocation(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"validate"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage:") {
		t.Fatalf("stderr = %q, want usage", stderr.String())
	}
}
