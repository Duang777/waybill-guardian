package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

func loadManifest(path string) (datasetManifest, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return datasetManifest{}, nil, fmt.Errorf("read manifest: %w", err)
	}
	var manifest datasetManifest
	if err := decodeStrict(raw, &manifest); err != nil {
		return datasetManifest{}, nil, fmt.Errorf("decode manifest: %w", err)
	}
	if manifest.SchemaVersion != manifestSchemaVersion {
		return datasetManifest{}, nil, fmt.Errorf(
			"manifest schema_version must be %q",
			manifestSchemaVersion,
		)
	}
	return manifest, raw, nil
}

func loadReport(path string) (benchmarkReport, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return benchmarkReport{}, nil, fmt.Errorf("read report: %w", err)
	}
	var report benchmarkReport
	if err := decodeStrict(raw, &report); err != nil {
		return benchmarkReport{}, nil, fmt.Errorf("decode report: %w", err)
	}
	if report.SchemaVersion != reportSchemaVersion {
		return benchmarkReport{}, nil, fmt.Errorf(
			"report schema_version must be %q",
			reportSchemaVersion,
		)
	}
	return report, raw, nil
}

func verifyReport(
	root string,
	report benchmarkReport,
	config suiteConfig,
	configRaw []byte,
	manifest datasetManifest,
	manifestRaw []byte,
) error {
	if report.ToolSHA256 != benchmarkToolDigest(root) {
		return fmt.Errorf("report tool_sha256 does not match scripts/delivery-benchmark")
	}
	if report.ConfigSHA256 != digestBytes(configRaw) {
		return fmt.Errorf("report config_sha256 does not match config.json")
	}
	if report.ManifestSHA256 != digestBytes(manifestRaw) {
		return fmt.Errorf("report manifest_sha256 does not match manifest.json")
	}
	if report.ReplayCount != config.ReplayCount ||
		report.SolveBudget != config.SolveBudgetEvaluations ||
		report.SolverTimeoutSecond != config.SolverTimeoutSeconds {
		return fmt.Errorf("report execution settings do not match config.json")
	}
	if !reflect.DeepEqual(report.Datasets, manifest.Datasets) {
		return fmt.Errorf("report dataset records do not match manifest.json")
	}
	if len(report.Adapters) != len(config.Adapters) {
		return fmt.Errorf("report has %d adapters, want %d", len(report.Adapters), len(config.Adapters))
	}
	for index, adapter := range report.Adapters {
		spec := config.Adapters[index]
		if adapter.ID != spec.ID ||
			adapter.Label != spec.Label ||
			adapter.Kind != spec.Kind ||
			adapter.CapabilityProfile != spec.CapabilityProfile ||
			adapter.RequiredForPublication != spec.RequiredForPublication {
			return fmt.Errorf("report adapter %d does not match config.json", index)
		}
		switch adapter.Status {
		case statusPassed:
			if len(adapter.Datasets) != len(config.Datasets) {
				return fmt.Errorf("passed adapter %q has incomplete datasets", adapter.ID)
			}
			for datasetIndex, evidence := range adapter.Datasets {
				if evidence.DatasetID != config.Datasets[datasetIndex].ID ||
					evidence.ProblemDigest != manifest.Datasets[datasetIndex].ProblemDigest {
					return fmt.Errorf("adapter %q dataset binding is invalid", adapter.ID)
				}
				if evidence.Status != statusPassed ||
					!evidence.AllValid ||
					!evidence.DigestStable ||
					evidence.HardViolationCount != 0 ||
					len(evidence.Runs) != config.ReplayCount {
					return fmt.Errorf("adapter %q dataset %q did not pass", adapter.ID, evidence.DatasetID)
				}
				for iteration, run := range evidence.Runs {
					if run.Iteration != iteration+1 ||
						!run.Valid ||
						run.HardViolationCount != 0 ||
						run.PlanDigest != evidence.PlanDigest ||
						run.ValidationDigest != evidence.ValidationDigest {
						return fmt.Errorf(
							"adapter %q dataset %q replay %d is invalid",
							adapter.ID,
							evidence.DatasetID,
							iteration+1,
						)
					}
				}
			}
		case statusBlocked:
			if adapter.Reason == "" || len(adapter.Datasets) != 0 {
				return fmt.Errorf("blocked adapter %q has invalid evidence", adapter.ID)
			}
		case statusFailed:
			if adapter.Reason == "" {
				return fmt.Errorf("failed adapter %q has no reason", adapter.ID)
			}
		default:
			return fmt.Errorf("adapter %q has unknown status %q", adapter.ID, adapter.Status)
		}
	}
	expectedGate := buildPublicationGate(report.Adapters)
	if !reflect.DeepEqual(report.Gate, expectedGate) {
		return fmt.Errorf("publication_gate does not match adapter evidence")
	}
	reference := report.Adapters[0]
	if reference.ID != "reference-baseline" || reference.Status != statusPassed {
		return fmt.Errorf("reference-baseline evidence did not pass")
	}
	return nil
}

func renderReport(report benchmarkReport, language string) (string, error) {
	switch language {
	case "zh":
		return renderChineseReport(report), nil
	case "en":
		return renderEnglishReport(report), nil
	default:
		return "", fmt.Errorf("unsupported report language %q", language)
	}
}

func renderChineseReport(report benchmarkReport) string {
	var output strings.Builder
	output.WriteString("# 城市配送基准证据\n\n")
	output.WriteString("> 此文件由 `go run ./scripts/delivery-benchmark report` 生成。")
	output.WriteString("权威数据位于 [`delivery-benchmark.v1.json`](./delivery-benchmark.v1.json)。\n\n")
	output.WriteString("## 执行环境\n\n")
	fmt.Fprintf(&output, "- 生成时间：`%s`\n", report.GeneratedAt.Format("2006-01-02T15:04:05Z"))
	fmt.Fprintf(&output, "- 源码修订：`%s`\n", report.SourceRevision)
	fmt.Fprintf(&output, "- 基准工具 SHA-256：`%s`\n", report.ToolSHA256)
	fmt.Fprintf(&output, "- Go：`%s`，平台：`%s/%s`\n",
		report.Environment.GoVersion, report.Environment.GOOS, report.Environment.GOARCH)
	fmt.Fprintf(&output, "- 操作系统：`%s`\n", report.Environment.OSVersion)
	fmt.Fprintf(&output, "- CPU：`%s`，内存：`%d` 字节\n",
		report.Environment.CPUModel, report.Environment.MemoryBytes)
	fmt.Fprintf(&output, "- 每个 adapter 和数据集重放：`%d` 次\n", report.ReplayCount)
	fmt.Fprintf(&output, "- 评估预算：`%d`，单次超时：`%d` 秒\n\n",
		report.SolveBudget, report.SolverTimeoutSecond)

	output.WriteString("## 固定数据集\n\n")
	output.WriteString("| 数据集 | seed | 请求 | 任务 | 货物 | 车辆 | canonical SHA-256 | problem digest |\n")
	output.WriteString("|---|---:|---:|---:|---:|---:|---|---|\n")
	for _, dataset := range report.Datasets {
		fmt.Fprintf(&output, "| `%s` | %d | %d | %d | %d | %d | `%s` | `%s` |\n",
			dataset.ID,
			dataset.Seed,
			dataset.RequestCount,
			dataset.TaskCount,
			dataset.CargoCount,
			dataset.VehicleCount,
			shortDigest(dataset.CanonicalSHA256),
			shortDigest(string(dataset.ProblemDigest)),
		)
	}

	output.WriteString("\n## Adapter 状态\n\n")
	output.WriteString("| Adapter | 状态 | 发布必需 | 说明 |\n")
	output.WriteString("|---|---|---:|---|\n")
	for _, adapter := range report.Adapters {
		fmt.Fprintf(&output, "| `%s` | `%s` | %s | %s |\n",
			adapter.ID,
			adapter.Status,
			yesNoZH(adapter.RequiredForPublication),
			tableText(adapter.Reason),
		)
	}

	output.WriteString("\n## 实测结果\n\n")
	output.WriteString("| Adapter | 数据集 | 可行率 | digest 一致 | 硬约束违规 | 车辆 | 总里程（米） | 总成本（分） | 准时率（ppm） | 求解 p50（毫秒） | 校验 p50（毫秒） | 校验 p95（毫秒） |\n")
	output.WriteString("|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	writeMeasuredRows(&output, report, "zh")

	output.WriteString("\n## 发布门禁\n\n")
	if report.Gate.PublicationReady {
		output.WriteString("所有发布必需 adapter 已通过固定数据、独立校验和 20 次摘要重放。\n")
	} else {
		output.WriteString("当前报告不能作为生产 solver 的发布质量证明。")
		output.WriteString("以下发布必需 adapter 尚未通过：")
		output.WriteString(codeList(report.Gate.BlockingAdapters))
		output.WriteString("。\n")
	}
	output.WriteString("\n报告只陈述固定数据和共同能力范围内的实测结果。")
	output.WriteString("它不证明全局最优，也不构成物理装载安全认证。\n")
	return output.String()
}

func renderEnglishReport(report benchmarkReport) string {
	var output strings.Builder
	output.WriteString("# City delivery benchmark evidence\n\n")
	output.WriteString("> `go run ./scripts/delivery-benchmark report` generates this file. ")
	output.WriteString("The authoritative data is [`delivery-benchmark.v1.json`](./delivery-benchmark.v1.json).\n\n")
	output.WriteString("## Runtime environment\n\n")
	fmt.Fprintf(&output, "- Generated at: `%s`\n", report.GeneratedAt.Format("2006-01-02T15:04:05Z"))
	fmt.Fprintf(&output, "- Source revision: `%s`\n", report.SourceRevision)
	fmt.Fprintf(&output, "- Benchmark tool SHA-256: `%s`\n", report.ToolSHA256)
	fmt.Fprintf(&output, "- Go: `%s`; platform: `%s/%s`\n",
		report.Environment.GoVersion, report.Environment.GOOS, report.Environment.GOARCH)
	fmt.Fprintf(&output, "- Operating system: `%s`\n", report.Environment.OSVersion)
	fmt.Fprintf(&output, "- CPU: `%s`; memory: `%d` bytes\n",
		report.Environment.CPUModel, report.Environment.MemoryBytes)
	fmt.Fprintf(&output, "- Replays per adapter and dataset: `%d`\n", report.ReplayCount)
	fmt.Fprintf(&output, "- Evaluation budget: `%d`; timeout per run: `%d` seconds\n\n",
		report.SolveBudget, report.SolverTimeoutSecond)

	output.WriteString("## Fixed datasets\n\n")
	output.WriteString("| Dataset | Seed | Requests | Tasks | Cargo | Vehicles | Canonical SHA-256 | Problem digest |\n")
	output.WriteString("|---|---:|---:|---:|---:|---:|---|---|\n")
	for _, dataset := range report.Datasets {
		fmt.Fprintf(&output, "| `%s` | %d | %d | %d | %d | %d | `%s` | `%s` |\n",
			dataset.ID,
			dataset.Seed,
			dataset.RequestCount,
			dataset.TaskCount,
			dataset.CargoCount,
			dataset.VehicleCount,
			shortDigest(dataset.CanonicalSHA256),
			shortDigest(string(dataset.ProblemDigest)),
		)
	}

	output.WriteString("\n## Adapter status\n\n")
	output.WriteString("| Adapter | Status | Required for publication | Detail |\n")
	output.WriteString("|---|---|---:|---|\n")
	for _, adapter := range report.Adapters {
		fmt.Fprintf(&output, "| `%s` | `%s` | %s | %s |\n",
			adapter.ID,
			adapter.Status,
			yesNoEN(adapter.RequiredForPublication),
			tableText(adapter.Reason),
		)
	}

	output.WriteString("\n## Measured results\n\n")
	output.WriteString("| Adapter | Dataset | Feasible replays | Stable digest | Hard violations | Vehicles | Distance (m) | Cost (cent) | On-time rate (ppm) | Solve p50 (ms) | Validate p50 (ms) | Validate p95 (ms) |\n")
	output.WriteString("|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	writeMeasuredRows(&output, report, "en")

	output.WriteString("\n## Publication gate\n\n")
	if report.Gate.PublicationReady {
		output.WriteString("Every required adapter passed the fixed datasets, independent validation, and 20 digest replays.\n")
	} else {
		output.WriteString("This report is not production solver release evidence. ")
		output.WriteString("These required adapters have not passed: ")
		output.WriteString(codeList(report.Gate.BlockingAdapters))
		output.WriteString(".\n")
	}
	output.WriteString("\nThe report covers measured behavior on the fixed datasets and common capability profile. ")
	output.WriteString("It does not prove global optimality or certify physical loading safety.\n")
	return output.String()
}

func writeMeasuredRows(output *strings.Builder, report benchmarkReport, language string) {
	rowCount := 0
	for _, adapter := range report.Adapters {
		if adapter.Status != statusPassed {
			continue
		}
		for _, dataset := range adapter.Datasets {
			rowCount++
			fmt.Fprintf(
				output,
				"| `%s` | `%s` | %d/%d | %s | %d | %d | %d | %d | %d | %.3f | %.3f | %.3f |\n",
				adapter.ID,
				dataset.DatasetID,
				countValidRuns(dataset.Runs),
				len(dataset.Runs),
				yesNo(dataset.DigestStable, language),
				dataset.HardViolationCount,
				dataset.Metrics.VehiclesUsed,
				dataset.Metrics.TotalDistanceMeters,
				dataset.Metrics.TotalCostCents,
				dataset.Metrics.OnTimeRatePPM,
				nanosecondsToMilliseconds(dataset.SolveDuration.Median),
				nanosecondsToMilliseconds(dataset.ValidationDuration.Median),
				nanosecondsToMilliseconds(dataset.ValidationDuration.P95),
			)
		}
	}
	if rowCount == 0 {
		if language == "zh" {
			output.WriteString("| 无 | 无 | 0/0 | 否 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |\n")
		} else {
			output.WriteString("| None | None | 0/0 | No | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |\n")
		}
	}
}

func countValidRuns(values []replayEvidence) int {
	count := 0
	for _, value := range values {
		if value.Valid && value.HardViolationCount == 0 {
			count++
		}
	}
	return count
}

func nanosecondsToMilliseconds(value int64) float64 {
	return float64(value) / float64(1_000_000)
}

func shortDigest(value string) string {
	if len(value) <= 16 {
		return value
	}
	return value[:16]
}

func yesNo(value bool, language string) string {
	if language == "zh" {
		return yesNoZH(value)
	}
	return yesNoEN(value)
}

func yesNoZH(value bool) string {
	if value {
		return "是"
	}
	return "否"
}

func yesNoEN(value bool) string {
	if value {
		return "Yes"
	}
	return "No"
}

func tableText(value string) string {
	if value == "" {
		return "-"
	}
	return strings.ReplaceAll(value, "|", "\\|")
}

func codeList(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = "`" + value + "`"
	}
	return strings.Join(quoted, ", ")
}

func writeAtomic(path string, raw []byte, permission os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(permission); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
