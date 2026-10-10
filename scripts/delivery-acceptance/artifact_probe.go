package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/artifact"
	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func runArtifactProbe(
	ctx context.Context,
	spec probeSpec,
	config artifactConfig,
) probeEvidence {
	evidence := probeEvidence{
		ID:                     spec.ID,
		Label:                  spec.Label,
		Kind:                   spec.Kind,
		RequiredForPublication: spec.RequiredForPublication,
		Status:                 statusFailed,
		Checks:                 []checkEvidence{},
		Measurements:           []measurementEvidence{},
		Artifacts:              []artifactEvidence{},
	}
	result, err := measureArtifacts(ctx, config)
	if err != nil {
		evidence.Reason = err.Error()
		return evidence
	}
	evidence.Checks = result.checks
	evidence.Measurements = result.measurements
	evidence.Artifacts = result.artifacts
	commandResult := commandResult{
		SchemaVersion: commandResultSchemaVersion,
		ProbeID:       spec.ID,
		Checks:        result.checks,
		Measurements:  []measurementEvidence{},
		Artifacts:     result.artifacts,
	}
	if err := validateEmbeddedChecks(spec, commandResult); err != nil {
		evidence.Reason = err.Error()
		return evidence
	}
	evidence.Status = statusPassed
	return evidence
}

type artifactProbeResult struct {
	checks       []checkEvidence
	measurements []measurementEvidence
	artifacts    []artifactEvidence
}

func measureArtifacts(
	ctx context.Context,
	config artifactConfig,
) (artifactProbeResult, error) {
	root, err := os.MkdirTemp("", "delivery-artifact-acceptance-")
	if err != nil {
		return artifactProbeResult{}, fmt.Errorf("create artifact work directory: %w", err)
	}
	defer os.RemoveAll(root)
	storeRoot := filepath.Join(root, "source")
	store, err := artifact.NewFileStore(storeRoot, config.MaxArtifactBytes)
	if err != nil {
		return artifactProbeResult{}, err
	}

	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)
	peakHeap := baseline.HeapAlloc
	baselineGoroutines := runtime.NumGoroutine()
	peakGoroutines := baselineGoroutines
	sampleRuntime := func() {
		var current runtime.MemStats
		runtime.ReadMemStats(&current)
		if current.HeapAlloc > peakHeap {
			peakHeap = current.HeapAlloc
		}
		if current := runtime.NumGoroutine(); current > peakGoroutines {
			peakGoroutines = current
		}
	}

	refs := make([]artifact.ArtifactRef, 0, config.Iterations)
	putDurations := make([]int64, 0, config.Iterations)
	openDurations := make([]int64, 0, config.Iterations)
	verifyDurations := make([]int64, 0, config.Iterations)
	for iteration := 0; iteration < config.Iterations; iteration++ {
		value, err := artifact.New(
			artifact.KindEvidence,
			"delivery.acceptance.evidence.v1",
			map[string]any{
				"iteration": int64(iteration + 1),
				"payload":   deterministicPayload(config.PayloadBytes, iteration),
			},
		)
		if err != nil {
			return artifactProbeResult{}, fmt.Errorf("construct artifact %d: %w", iteration+1, err)
		}
		startedAt := time.Now()
		ref, err := store.Put(ctx, "tenant-a", value)
		putDurations = append(putDurations, time.Since(startedAt).Nanoseconds())
		if err != nil {
			return artifactProbeResult{}, fmt.Errorf("put artifact %d: %w", iteration+1, err)
		}
		refs = append(refs, ref)

		startedAt = time.Now()
		reader, metadata, err := store.Open(ctx, "tenant-a", ref.Digest)
		if err == nil {
			_, err = io.Copy(io.Discard, reader)
			err = errors.Join(err, reader.Close())
		}
		openDurations = append(openDurations, time.Since(startedAt).Nanoseconds())
		if err != nil {
			return artifactProbeResult{}, fmt.Errorf("open artifact %d: %w", iteration+1, err)
		}
		if metadata.Digest != ref.Digest || metadata.SizeBytes != ref.SizeBytes {
			return artifactProbeResult{}, fmt.Errorf("artifact %d metadata changed", iteration+1)
		}

		startedAt = time.Now()
		err = store.Verify(ctx, "tenant-a", ref.Digest)
		verifyDurations = append(verifyDurations, time.Since(startedAt).Nanoseconds())
		if err != nil {
			return artifactProbeResult{}, fmt.Errorf("verify artifact %d: %w", iteration+1, err)
		}
		sampleRuntime()
	}

	tenantIsolated := false
	if _, _, err := store.Open(ctx, "tenant-b", refs[0].Digest); errors.Is(err, artifact.ErrNotFound) {
		tenantIsolated = true
	}

	sizeLimited := false
	oversize, err := artifact.New(
		artifact.KindEvidence,
		"delivery.acceptance.evidence.v1",
		map[string]any{"payload": strings.Repeat("z", config.OversizeBytes)},
	)
	if err != nil {
		return artifactProbeResult{}, fmt.Errorf("construct oversize artifact: %w", err)
	}
	if _, err := store.Put(ctx, "tenant-a", oversize); errors.Is(err, artifact.ErrTooLarge) {
		sizeLimited = true
	}

	cancelled := false
	cancelContext, cancel := context.WithCancel(ctx)
	cancel()
	small, err := artifact.New(
		artifact.KindEvidence,
		"delivery.acceptance.evidence.v1",
		map[string]any{"cancelled": true},
	)
	if err != nil {
		return artifactProbeResult{}, err
	}
	if _, err := store.Put(cancelContext, "tenant-a", small); errors.Is(err, context.Canceled) {
		cancelled = true
	}

	concurrentValue, err := artifact.New(
		artifact.KindEvidence,
		"delivery.acceptance.evidence.v1",
		map[string]any{"concurrent": true},
	)
	if err != nil {
		return artifactProbeResult{}, err
	}
	concurrentOK := true
	var group sync.WaitGroup
	errs := make(chan error, config.ConcurrentWriters)
	for range config.ConcurrentWriters {
		group.Add(1)
		go func() {
			defer group.Done()
			ref, putErr := store.Put(ctx, "tenant-a", concurrentValue)
			if putErr == nil && ref.Digest != concurrentValue.Digest() {
				putErr = fmt.Errorf("concurrent digest changed")
			}
			errs <- putErr
		}()
	}
	sampleRuntime()
	group.Wait()
	close(errs)
	for concurrentErr := range errs {
		if concurrentErr != nil {
			concurrentOK = false
			break
		}
	}
	sampleRuntime()

	sourceSHA, sourceBytes, privatePermissions, err := digestArtifactTree(storeRoot)
	if err != nil {
		return artifactProbeResult{}, err
	}
	backupRoot := filepath.Join(root, "backup")
	restoreRoot := filepath.Join(root, "restore")
	recoveryStartedAt := time.Now()
	if err := copyDirectory(storeRoot, backupRoot); err != nil {
		return artifactProbeResult{}, fmt.Errorf("back up artifact tree: %w", err)
	}
	if err := copyDirectory(backupRoot, restoreRoot); err != nil {
		return artifactProbeResult{}, fmt.Errorf("restore artifact tree: %w", err)
	}
	restoredStore, err := artifact.NewFileStore(restoreRoot, config.MaxArtifactBytes)
	if err != nil {
		return artifactProbeResult{}, fmt.Errorf("open restored artifact store: %w", err)
	}
	for _, ref := range refs {
		if err := restoredStore.Verify(ctx, "tenant-a", ref.Digest); err != nil {
			return artifactProbeResult{}, fmt.Errorf("verify restored artifact %s: %w", ref.Digest, err)
		}
	}
	if err := restoredStore.Verify(ctx, "tenant-a", concurrentValue.Digest()); err != nil {
		return artifactProbeResult{}, fmt.Errorf("verify restored concurrent artifact: %w", err)
	}
	restoreSHA, restoreBytes, restoredPrivate, err := digestArtifactTree(restoreRoot)
	if err != nil {
		return artifactProbeResult{}, err
	}
	recoveryDuration := time.Since(recoveryStartedAt).Nanoseconds()
	backupRestored := sourceSHA == restoreSHA && sourceBytes == restoreBytes
	privatePermissions = privatePermissions && restoredPrivate

	corruptionDetected := false
	corruptPath, err := findArtifactPath(storeRoot, refs[0].Digest)
	if err != nil {
		return artifactProbeResult{}, err
	}
	if err := os.WriteFile(corruptPath, []byte(`{"tampered":true}`), 0o600); err != nil {
		return artifactProbeResult{}, fmt.Errorf("corrupt artifact: %w", err)
	}
	if err := store.Verify(ctx, "tenant-a", refs[0].Digest); errors.Is(err, artifact.ErrIntegrity) {
		corruptionDetected = true
	}

	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	sampleRuntime()
	finalGoroutines := runtime.NumGoroutine()
	peakHeapBytes := peakHeap - baseline.HeapAlloc
	if peakHeapBytes == 0 {
		peakHeapBytes = 1
	}
	boundedHeap := peakHeapBytes <= config.MaxPeakHeapBytes
	boundedGoroutines := finalGoroutines <= baselineGoroutines+4
	recoveryWithinLimit := recoveryDuration <= config.MaxRecoveryNS

	checks := []checkEvidence{
		check("tenant_isolation", tenantIsolated, "a foreign tenant could not open the digest"),
		check("digest_integrity", len(refs) == config.Iterations, "all stored digests reopened and verified"),
		check("corruption_detected", corruptionDetected, "tampered envelope returned ErrIntegrity"),
		check("backup_restore", backupRestored && recoveryWithinLimit,
			fmt.Sprintf("source and restored trees match; recovery_ns=%d", recoveryDuration)),
		check("size_limit", sizeLimited, "oversize artifact returned ErrTooLarge"),
		check("cancellation", cancelled, "cancelled context stopped publication"),
		check("concurrent_put", concurrentOK, "concurrent writers converged on one digest"),
		check("bounded_heap", boundedHeap,
			fmt.Sprintf("peak_heap_bytes=%d limit=%d", peakHeapBytes, config.MaxPeakHeapBytes)),
		check("bounded_goroutines", boundedGoroutines,
			fmt.Sprintf("baseline=%d peak=%d final=%d",
				baselineGoroutines, peakGoroutines, finalGoroutines)),
		check("private_permissions", privatePermissions, "artifact files and directories deny group and other access"),
	}
	for _, value := range checks {
		if !value.Passed {
			return artifactProbeResult{}, fmt.Errorf("%s: %s", value.ID, value.Detail)
		}
	}

	measurements := []measurementEvidence{
		artifactMeasurement("artifact-put", putDurations, peakHeapBytes, peakGoroutines),
		artifactMeasurement("artifact-open", openDurations, peakHeapBytes, peakGoroutines),
		artifactMeasurement("artifact-verify", verifyDurations, peakHeapBytes, peakGoroutines),
		{
			ID:              "artifact-restore",
			SampleCount:     1,
			DurationNS:      summarizeDurations([]int64{recoveryDuration}),
			PeakMemoryBytes: peakHeapBytes,
			PeakGoroutines:  peakGoroutines,
			FeasibilityPPM:  1_000_000,
			RecoveryNS:      recoveryDuration,
		},
	}
	return artifactProbeResult{
		checks:       checks,
		measurements: measurements,
		artifacts: []artifactEvidence{{
			Name:   "artifact-tree",
			SHA256: sourceSHA,
			Bytes:  sourceBytes,
		}},
	}, nil
}

func validateEmbeddedChecks(spec probeSpec, result commandResult) error {
	checks := make(map[string]checkEvidence, len(result.Checks))
	for _, value := range result.Checks {
		checks[value.ID] = value
	}
	for _, expected := range spec.ExpectedChecks {
		value, exists := checks[expected]
		if !exists {
			return fmt.Errorf("missing check %q", expected)
		}
		if !value.Passed {
			return fmt.Errorf("check %q did not pass: %s", expected, value.Detail)
		}
	}
	if len(checks) != len(spec.ExpectedChecks) {
		return fmt.Errorf("embedded probe returned undeclared checks")
	}
	return nil
}

func check(id string, passed bool, detail string) checkEvidence {
	return checkEvidence{ID: id, Passed: passed, Detail: detail}
}

func artifactMeasurement(
	id string,
	durations []int64,
	peakHeap uint64,
	peakGoroutines int,
) measurementEvidence {
	return measurementEvidence{
		ID:              id,
		SampleCount:     len(durations),
		DurationNS:      summarizeDurations(durations),
		PeakMemoryBytes: peakHeap,
		PeakGoroutines:  peakGoroutines,
		FeasibilityPPM:  1_000_000,
	}
}

func summarizeDurations(values []int64) durationSummary {
	ordered := append([]int64(nil), values...)
	slices.Sort(ordered)
	return durationSummary{
		Minimum: ordered[0],
		P50:     nearestRank(ordered, 50),
		P95:     nearestRank(ordered, 95),
		P99:     nearestRank(ordered, 99),
		Maximum: ordered[len(ordered)-1],
	}
}

func nearestRank(ordered []int64, percentile int) int64 {
	index := (len(ordered)*percentile + 99) / 100
	if index < 1 {
		index = 1
	}
	return ordered[index-1]
}

func deterministicPayload(size int, iteration int) string {
	alphabet := "0123456789abcdefghijklmnopqrstuvwxyz"
	value := make([]byte, size)
	for index := range value {
		value[index] = alphabet[(index+iteration)%len(alphabet)]
	}
	return string(value)
}

func digestArtifactTree(root string) (string, int64, bool, error) {
	hasher := sha256.New()
	var total int64
	private := true
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0o077 != 0 {
			private = false
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		_, _ = io.WriteString(hasher, filepath.ToSlash(relative))
		_, _ = io.WriteString(hasher, "\x00")
		if entry.IsDir() {
			_, _ = io.WriteString(hasher, "directory\x00")
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("artifact tree contains non-regular file %s", relative)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		total += int64(len(raw))
		_, _ = hasher.Write(raw)
		_, _ = io.WriteString(hasher, "\x00")
		return nil
	})
	if err != nil {
		return "", 0, false, fmt.Errorf("digest artifact tree: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), total, private, nil
}

func copyDirectory(source string, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(destination, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refuse to copy non-regular file %s", path)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(
			destination,
			os.O_WRONLY|os.O_CREATE|os.O_EXCL,
			info.Mode().Perm(),
		)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		syncErr := output.Sync()
		closeErr := output.Close()
		return errors.Join(copyErr, syncErr, closeErr)
	})
}

func findArtifactPath(root string, digest domain.ArtifactDigest) (string, error) {
	suffix := string(digest) + ".artifact"
	var found string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && entry.Name() == suffix {
			found = path
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("artifact %s was not found on disk", digest)
	}
	return found, nil
}
