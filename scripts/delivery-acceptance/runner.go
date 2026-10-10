package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

func runAcceptance(
	ctx context.Context,
	root string,
	configPath string,
	config suiteConfig,
	configRaw []byte,
	skipBrowser bool,
) acceptanceReport {
	report := acceptanceReport{
		SchemaVersion:  reportSchemaVersion,
		GeneratedAt:    time.Now().UTC().Truncate(time.Second),
		ToolSHA256:     acceptanceToolDigest(root),
		ConfigSHA256:   digestBytes(configRaw),
		SubjectSHA256:  acceptanceSubjectDigest(root),
		SourceRevision: gitRevision(ctx, root),
		Environment:    inspectEnvironment(ctx),
		Probes:         make([]probeEvidence, 0, len(config.Probes)),
	}
	for _, spec := range config.Probes {
		var evidence probeEvidence
		switch spec.Kind {
		case "embedded_artifact":
			evidence = runArtifactProbe(ctx, spec, config.Artifact)
		case "browser":
			evidence = runBrowserProbe(ctx, root, spec, config.Browser, skipBrowser)
		case "command":
			evidence = runCommandProbe(
				ctx,
				root,
				configPath,
				time.Duration(config.CommandTimeoutSeconds)*time.Second,
				spec,
			)
		default:
			evidence = probeEvidence{
				ID:                     spec.ID,
				Label:                  spec.Label,
				Kind:                   spec.Kind,
				RequiredForPublication: spec.RequiredForPublication,
				Status:                 statusFailed,
				Reason:                 "unsupported probe kind",
			}
		}
		report.Probes = append(report.Probes, evidence)
	}
	report.Gate = buildPublicationGate(report.Probes)
	return report
}

func loadReport(path string) (acceptanceReport, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return acceptanceReport{}, nil, fmt.Errorf("read report: %w", err)
	}
	var report acceptanceReport
	if err := decodeStrict(raw, &report); err != nil {
		return acceptanceReport{}, nil, fmt.Errorf("decode report: %w", err)
	}
	if report.SchemaVersion != reportSchemaVersion {
		return acceptanceReport{}, nil, fmt.Errorf(
			"report schema_version must be %q",
			reportSchemaVersion,
		)
	}
	return report, raw, nil
}

func verifyReport(
	root string,
	config suiteConfig,
	configRaw []byte,
	report acceptanceReport,
) error {
	if report.ToolSHA256 != acceptanceToolDigest(root) {
		return fmt.Errorf("report tool_sha256 does not match scripts/delivery-acceptance")
	}
	if report.ConfigSHA256 != digestBytes(configRaw) {
		return fmt.Errorf("report config_sha256 does not match config.json")
	}
	if report.SubjectSHA256 != acceptanceSubjectDigest(root) {
		return fmt.Errorf("report subject_sha256 does not match the tested source files")
	}
	if report.GeneratedAt.IsZero() || report.SourceRevision == "" {
		return fmt.Errorf("report execution identity is incomplete")
	}
	if len(report.Probes) != len(config.Probes) {
		return fmt.Errorf("report has %d probes, want %d", len(report.Probes), len(config.Probes))
	}
	for index, evidence := range report.Probes {
		spec := config.Probes[index]
		if evidence.ID != spec.ID ||
			evidence.Label != spec.Label ||
			evidence.Kind != spec.Kind ||
			evidence.RequiredForPublication != spec.RequiredForPublication {
			return fmt.Errorf("report probe %d does not match config.json", index)
		}
		switch evidence.Status {
		case statusPassed:
			if evidence.Reason != "" {
				return fmt.Errorf("passed probe %q has a failure reason", evidence.ID)
			}
			result := commandResult{
				SchemaVersion: commandResultSchemaVersion,
				ProbeID:       evidence.ID,
				Checks:        evidence.Checks,
				Measurements:  evidence.Measurements,
				Artifacts:     evidence.Artifacts,
			}
			if spec.Kind == "command" {
				if err := validateCommandResult(spec, result); err != nil {
					return fmt.Errorf("probe %q: %w", evidence.ID, err)
				}
			} else {
				if err := validateEmbeddedChecks(spec, result); err != nil {
					return fmt.Errorf("probe %q: %w", evidence.ID, err)
				}
				for _, measurement := range evidence.Measurements {
					if err := validateMeasurement(measurement); err != nil {
						return fmt.Errorf(
							"probe %q measurement %q: %w",
							evidence.ID,
							measurement.ID,
							err,
						)
					}
				}
				for _, item := range evidence.Artifacts {
					if item.Name == "" || len(item.SHA256) != 64 || item.Bytes <= 0 {
						return fmt.Errorf("probe %q has invalid artifact evidence", evidence.ID)
					}
				}
			}
		case statusBlocked:
			if evidence.Reason == "" ||
				len(evidence.Checks) != 0 ||
				len(evidence.Measurements) != 0 ||
				len(evidence.Artifacts) != 0 {
				return fmt.Errorf("blocked probe %q has invalid evidence", evidence.ID)
			}
		case statusFailed:
			if evidence.Reason == "" {
				return fmt.Errorf("failed probe %q has no reason", evidence.ID)
			}
		default:
			return fmt.Errorf("probe %q has unknown status %q", evidence.ID, evidence.Status)
		}
	}
	expectedGate := buildPublicationGate(report.Probes)
	if !reflect.DeepEqual(report.Gate, expectedGate) {
		return fmt.Errorf("publication_gate does not match probe evidence")
	}
	return nil
}

func inspectEnvironment(ctx context.Context) runtimeEnvironment {
	return runtimeEnvironment{
		GOOS:        runtime.GOOS,
		GOARCH:      runtime.GOARCH,
		GoVersion:   runtime.Version(),
		OSVersion:   firstCommandOutput(ctx, []string{"uname", "-sr"}),
		CPUModel:    cpuModel(ctx),
		MemoryBytes: memoryBytes(ctx),
	}
}

func cpuModel(ctx context.Context) string {
	if runtime.GOOS == "darwin" {
		return firstCommandOutput(ctx, []string{"sysctl", "-n", "machdep.cpu.brand_string"})
	}
	raw, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return "unavailable"
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "model name") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return "unavailable"
}

func memoryBytes(ctx context.Context) uint64 {
	if runtime.GOOS == "darwin" {
		value := firstCommandOutput(ctx, []string{"sysctl", "-n", "hw.memsize"})
		result, _ := strconv.ParseUint(value, 10, 64)
		return result
	}
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kib, _ := strconv.ParseUint(fields[1], 10, 64)
				return kib * 1024
			}
		}
	}
	return 0
}

func gitRevision(ctx context.Context, root string) string {
	process := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	process.Dir = root
	raw, err := process.Output()
	if err != nil {
		return "unavailable"
	}
	return strings.TrimSpace(string(raw))
}

func firstCommandOutput(ctx context.Context, command []string) string {
	if len(command) == 0 {
		return "unavailable"
	}
	process := exec.CommandContext(ctx, command[0], command[1:]...)
	raw, err := process.Output()
	if err != nil {
		return "unavailable"
	}
	return strings.TrimSpace(string(raw))
}

func acceptanceToolDigest(root string) string {
	return digestPaths(root, []string{"scripts/delivery-acceptance"}, func(path string) bool {
		switch filepath.Ext(path) {
		case ".go", ".json", ".sh":
			return true
		default:
			return false
		}
	})
}

func acceptanceSubjectDigest(root string) string {
	return digestPaths(root, []string{
		".github/workflows/ci.yml",
		"Dockerfile",
		"docs/licenses",
		"go.mod",
		"go.sum",
		"internal/delivery/artifact",
		"scripts/licenses.sh",
		"web/package-lock.json",
		"web/package.json",
		"web/scripts/verify-delivery.mjs",
		"web/src/delivery",
	}, func(path string) bool {
		extension := filepath.Ext(path)
		return extension != ".png" && extension != ".map"
	})
}

func digestPaths(
	root string,
	subjects []string,
	include func(string) bool,
) string {
	paths := make([]string, 0)
	for _, subject := range subjects {
		path := filepath.Join(root, filepath.FromSlash(subject))
		info, err := os.Stat(path)
		if err != nil {
			return "unavailable"
		}
		if !info.IsDir() {
			if include(path) {
				paths = append(paths, path)
			}
			continue
		}
		err = filepath.WalkDir(path, func(
			current string,
			entry os.DirEntry,
			entryErr error,
		) error {
			if entryErr != nil {
				return entryErr
			}
			if !entry.IsDir() && include(current) {
				paths = append(paths, current)
			}
			return nil
		})
		if err != nil {
			return "unavailable"
		}
	}
	slices.Sort(paths)
	hash := sha256.New()
	for _, path := range paths {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return "unavailable"
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "unavailable"
		}
		_, _ = hash.Write([]byte(filepath.ToSlash(relative)))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(raw)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func digestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func marshalIndented(value any) ([]byte, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func compareFile(path string, expected []byte, label string) error {
	actual, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", label, err)
	}
	if !bytes.Equal(actual, expected) {
		return fmt.Errorf("%s is stale: %s", label, path)
	}
	return nil
}
