package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/delivery/validate"
)

func runBenchmark(
	ctx context.Context,
	root string,
	config suiteConfig,
	configRaw []byte,
	datasets []generatedDataset,
	manifest datasetManifest,
	manifestRaw []byte,
) benchmarkReport {
	report := benchmarkReport{
		SchemaVersion:       reportSchemaVersion,
		GeneratedAt:         time.Now().UTC().Truncate(time.Second),
		ToolSHA256:          benchmarkToolDigest(root),
		ConfigSHA256:        digestBytes(configRaw),
		ManifestSHA256:      digestBytes(manifestRaw),
		SourceRevision:      gitRevision(ctx, root),
		ReplayCount:         config.ReplayCount,
		SolveBudget:         config.SolveBudgetEvaluations,
		SolverTimeoutSecond: config.SolverTimeoutSeconds,
		Environment:         inspectEnvironment(ctx),
		Datasets:            append([]datasetManifestRecord(nil), manifest.Datasets...),
		Adapters:            make([]adapterEvidence, 0, len(config.Adapters)),
	}
	base, _ := time.Parse(time.RFC3339, config.Generator.BaseTime)
	for _, adapter := range config.Adapters {
		report.Adapters = append(
			report.Adapters,
			runAdapter(ctx, root, config, adapter, datasets, base.Add(-time.Minute)),
		)
	}
	report.Gate = buildPublicationGate(report.Adapters)
	return report
}

func runAdapter(
	ctx context.Context,
	root string,
	config suiteConfig,
	spec adapterSpec,
	datasets []generatedDataset,
	validationTime time.Time,
) adapterEvidence {
	evidence := adapterEvidence{
		ID:                     spec.ID,
		Label:                  spec.Label,
		Kind:                   spec.Kind,
		CapabilityProfile:      spec.CapabilityProfile,
		RequiredForPublication: spec.RequiredForPublication,
		Datasets:               []datasetEvidence{},
	}
	var command []string
	if spec.Kind == "command" {
		raw := strings.TrimSpace(os.Getenv(spec.CommandEnv))
		if raw == "" {
			evidence.Status = statusBlocked
			evidence.Reason = "command environment variable " + spec.CommandEnv + " is not set"
			evidence.CommandSource = spec.CommandEnv
			return evidence
		}
		if err := decodeStrict([]byte(raw), &command); err != nil {
			evidence.Status = statusFailed
			evidence.Reason = "command environment variable is not a JSON string array"
			evidence.CommandSource = spec.CommandEnv
			return evidence
		}
		if err := validateCommandTemplate(command); err != nil {
			evidence.Status = statusFailed
			evidence.Reason = err.Error()
			evidence.CommandSource = spec.CommandEnv
			return evidence
		}
		evidence.CommandSource = spec.CommandEnv
	}

	evidence.Status = statusPassed
	for _, dataset := range datasets {
		result := runDataset(
			ctx,
			root,
			config,
			spec,
			command,
			dataset,
			validationTime,
		)
		evidence.Datasets = append(evidence.Datasets, result)
		if result.Status == statusFailed {
			evidence.Status = statusFailed
			if evidence.Reason == "" {
				evidence.Reason = result.DatasetID + ": " + result.Reason
			}
		}
	}
	return evidence
}

func runDataset(
	ctx context.Context,
	root string,
	config suiteConfig,
	adapter adapterSpec,
	command []string,
	dataset generatedDataset,
	validationTime time.Time,
) datasetEvidence {
	result := datasetEvidence{
		DatasetID:     dataset.spec.ID,
		ProblemDigest: dataset.problem.ProblemDigest,
		Status:        statusPassed,
		AllValid:      true,
		DigestStable:  true,
		Runs:          make([]replayEvidence, 0, config.ReplayCount),
	}
	tempDir, err := os.MkdirTemp("", "delivery-benchmark-"+dataset.spec.ID+"-")
	if err != nil {
		result.Status = statusFailed
		result.Reason = "create benchmark work directory"
		return result
	}
	defer os.RemoveAll(tempDir)
	problemPath := filepath.Join(tempDir, "problem.json")
	if err := os.WriteFile(problemPath, append(dataset.canonical, '\n'), 0o600); err != nil {
		result.Status = statusFailed
		result.Reason = "write benchmark problem"
		return result
	}
	validator := validate.New(config.Validator)
	solveDurations := make([]int64, 0, config.ReplayCount)
	validationDurations := make([]int64, 0, config.ReplayCount)
	for iteration := 1; iteration <= config.ReplayCount; iteration++ {
		startedAt := time.Now()
		plan, solveErr := solveOnce(
			ctx,
			root,
			config,
			adapter,
			command,
			dataset,
			problemPath,
			tempDir,
			iteration,
		)
		solveDuration := time.Since(startedAt).Nanoseconds()
		if solveErr != nil {
			result.Status = statusFailed
			result.AllValid = false
			result.DigestStable = false
			result.Reason = solveErr.Error()
			break
		}
		validationStartedAt := time.Now()
		validation := validator.Validate(dataset.problem, plan, validationTime)
		validationDuration := time.Since(validationStartedAt).Nanoseconds()
		hardViolations := countHardViolations(validation)
		current := replayEvidence{
			Iteration:            iteration,
			SolveDurationNS:      solveDuration,
			ValidationDurationNS: validationDuration,
			PlanDigest:           plan.PlanDigest,
			ValidationDigest:     validation.ReportDigest,
			Valid:                validation.Valid,
			HardViolationCount:   hardViolations,
			AssignedUnits:        validation.Metrics.AssignedUnits,
			UnassignedUnits:      validation.Metrics.UnassignedUnits,
			TotalDistanceMeters:  validation.Metrics.TotalDistanceMeters,
			TotalCostCents:       validation.Metrics.TotalCostCents,
		}
		result.Runs = append(result.Runs, current)
		solveDurations = append(solveDurations, solveDuration)
		validationDurations = append(validationDurations, validationDuration)
		if iteration == 1 {
			result.PlanDigest = plan.PlanDigest
			result.ValidationDigest = validation.ReportDigest
			result.Metrics = validation.Metrics
		} else if result.PlanDigest != plan.PlanDigest ||
			result.ValidationDigest != validation.ReportDigest {
			result.DigestStable = false
		}
		if !validation.Valid || hardViolations != 0 {
			result.AllValid = false
			result.HardViolationCount += hardViolations
		}
	}
	if len(solveDurations) > 0 {
		result.SolveDuration = summarizeDurations(solveDurations)
		result.ValidationDuration = summarizeDurations(validationDurations)
	}
	if len(result.Runs) != config.ReplayCount {
		result.Status = statusFailed
		if result.Reason == "" {
			result.Reason = fmt.Sprintf(
				"completed %d of %d required replays",
				len(result.Runs),
				config.ReplayCount,
			)
		}
	}
	if !result.AllValid || !result.DigestStable {
		result.Status = statusFailed
		if result.Reason == "" {
			result.Reason = "validation failed or plan digest changed across replays"
		}
	}
	return result
}

func solveOnce(
	ctx context.Context,
	root string,
	config suiteConfig,
	adapter adapterSpec,
	command []string,
	dataset generatedDataset,
	problemPath string,
	tempDir string,
	iteration int,
) (domain.Plan, error) {
	if adapter.Kind == "embedded_reference" {
		return buildReferencePlan(dataset.problem, dataset.spec.ID)
	}
	outputPath := filepath.Join(tempDir, fmt.Sprintf("plan-%02d.json", iteration))
	args := expandCommand(command, map[string]string{
		"{problem}":         problemPath,
		"{plan}":            outputPath,
		"{dataset}":         dataset.spec.ID,
		"{budget}":          strconv.FormatUint(config.SolveBudgetEvaluations, 10),
		"{timeout_seconds}": strconv.Itoa(config.SolverTimeoutSeconds),
	})
	runContext, cancel := context.WithTimeout(
		ctx,
		time.Duration(config.SolverTimeoutSeconds)*time.Second,
	)
	defer cancel()
	process := exec.CommandContext(runContext, args[0], args[1:]...)
	process.Dir = root
	var stdout boundedBuffer
	var stderr boundedBuffer
	process.Stdout = &stdout
	process.Stderr = &stderr
	if err := process.Run(); err != nil {
		if errors.Is(runContext.Err(), context.DeadlineExceeded) {
			return domain.Plan{}, fmt.Errorf(
				"solver exceeded %d second timeout",
				config.SolverTimeoutSeconds,
			)
		}
		return domain.Plan{}, fmt.Errorf("solver command failed: %s", publicProcessError(err))
	}
	raw, err := os.ReadFile(outputPath)
	if err != nil {
		return domain.Plan{}, fmt.Errorf("solver did not write the plan output")
	}
	var plan domain.Plan
	if err := decodeStrict(raw, &plan); err != nil {
		return domain.Plan{}, fmt.Errorf("solver plan is not valid delivery.plan.v1 JSON")
	}
	return plan, nil
}

func validateCommandTemplate(command []string) error {
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return fmt.Errorf("solver command array is empty")
	}
	joined := strings.Join(command, "\x00")
	if !strings.Contains(joined, "{problem}") || !strings.Contains(joined, "{plan}") {
		return fmt.Errorf("solver command must contain {problem} and {plan} placeholders")
	}
	return nil
}

func expandCommand(command []string, replacements map[string]string) []string {
	result := make([]string, len(command))
	for index, argument := range command {
		result[index] = argument
		for marker, value := range replacements {
			result[index] = strings.ReplaceAll(result[index], marker, value)
		}
	}
	return result
}

type boundedBuffer struct {
	value bytes.Buffer
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	const limit = 64 * 1024
	remaining := limit - buffer.value.Len()
	if remaining > 0 {
		chunk := value
		if len(chunk) > remaining {
			chunk = chunk[:remaining]
		}
		_, _ = buffer.value.Write(chunk)
	}
	return len(value), nil
}

func publicProcessError(err error) string {
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return fmt.Sprintf("exit code %d", exitError.ExitCode())
	}
	return "process could not start"
}

func summarizeDurations(values []int64) durationSummary {
	ordered := append([]int64(nil), values...)
	slices.Sort(ordered)
	p95Index := (len(ordered)*95 + 99) / 100
	if p95Index == 0 {
		p95Index = 1
	}
	return durationSummary{
		Minimum: ordered[0],
		Median:  ordered[(len(ordered)-1)/2],
		P95:     ordered[p95Index-1],
		Maximum: ordered[len(ordered)-1],
	}
}

func buildPublicationGate(adapters []adapterEvidence) publicationGate {
	gate := publicationGate{
		PublicationReady: true,
		BlockingAdapters: []string{},
		FailedAdapters:   []string{},
	}
	for _, adapter := range adapters {
		if adapter.ID == "reference-baseline" {
			gate.ReferenceEvidencePassed = adapter.Status == statusPassed
		}
		if adapter.Status == statusFailed {
			gate.FailedAdapters = append(gate.FailedAdapters, adapter.ID)
		}
		if adapter.RequiredForPublication && adapter.Status != statusPassed {
			gate.PublicationReady = false
			gate.BlockingAdapters = append(gate.BlockingAdapters, adapter.ID)
		}
	}
	if !gate.ReferenceEvidencePassed {
		gate.PublicationReady = false
	}
	slices.Sort(gate.BlockingAdapters)
	slices.Sort(gate.FailedAdapters)
	return gate
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
	command := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	command.Dir = root
	raw, err := command.Output()
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

func digestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func benchmarkToolDigest(root string) string {
	base := filepath.Join(root, "scripts", "delivery-benchmark")
	paths := make([]string, 0)
	walkErr := filepath.WalkDir(base, func(path string, entry os.DirEntry, entryErr error) error {
		if entryErr != nil {
			return entryErr
		}
		if entry.IsDir() {
			return nil
		}
		extension := filepath.Ext(path)
		if extension == ".go" || extension == ".json" || extension == ".sh" {
			paths = append(paths, path)
		}
		return nil
	})
	if walkErr != nil {
		return "unavailable"
	}
	slices.Sort(paths)
	hash := sha256.New()
	for _, path := range paths {
		relative, _ := filepath.Rel(root, path)
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

func marshalIndented(value any) ([]byte, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}
