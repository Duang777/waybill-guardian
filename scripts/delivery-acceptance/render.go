package main

import (
	"fmt"
	"strings"
)

func renderReport(report acceptanceReport, language string) (string, error) {
	switch language {
	case "zh":
		return renderChineseReport(report), nil
	case "en":
		return renderEnglishReport(report), nil
	default:
		return "", fmt.Errorf("unsupported report language %q", language)
	}
}

func renderChineseReport(report acceptanceReport) string {
	var output strings.Builder
	output.WriteString("# 城市配送生产验收报告\n\n")
	output.WriteString("> 此文件由 `go run ./scripts/delivery-acceptance report` 生成。")
	output.WriteString("权威数据位于 [`delivery-acceptance.v1.json`](./delivery-acceptance.v1.json)。\n\n")
	output.WriteString("## 执行环境\n\n")
	fmt.Fprintf(&output, "- 生成时间：`%s`\n", report.GeneratedAt.Format("2006-01-02T15:04:05Z"))
	fmt.Fprintf(&output, "- 源码修订：`%s`\n", report.SourceRevision)
	fmt.Fprintf(&output, "- 验收工具 SHA-256：`%s`\n", report.ToolSHA256)
	fmt.Fprintf(&output, "- 被测源码 SHA-256：`%s`\n", report.SubjectSHA256)
	fmt.Fprintf(&output, "- Go：`%s`，平台：`%s/%s`\n",
		report.Environment.GoVersion, report.Environment.GOOS, report.Environment.GOARCH)
	fmt.Fprintf(&output, "- 操作系统：`%s`\n", report.Environment.OSVersion)
	fmt.Fprintf(&output, "- CPU：`%s`，内存：`%d` 字节\n\n",
		report.Environment.CPUModel, report.Environment.MemoryBytes)

	output.WriteString("## 探针状态\n\n")
	output.WriteString("| 探针 | 状态 | 发布必需 | 说明 |\n")
	output.WriteString("|---|---|---:|---|\n")
	for _, probe := range report.Probes {
		fmt.Fprintf(&output, "| `%s` | `%s` | %s | %s |\n",
			probe.ID,
			probe.Status,
			yesNoZH(probe.RequiredForPublication),
			tableText(probe.Reason),
		)
	}

	output.WriteString("\n## 容量与恢复数据\n\n")
	output.WriteString("| 探针 | 场景 | 样本 | 任务/车/仓/每车货物 | 查询/SSE | p50 | p95 | p99 | 峰值内存 | 可行率 | 质量差距 | 恢复时间 |\n")
	output.WriteString("|---|---|---:|---|---|---:|---:|---:|---:|---:|---:|---:|\n")
	writeMeasurementRows(&output, report)

	output.WriteString("\n## 检查结果\n\n")
	output.WriteString("| 探针 | 检查 | 结果 | 证据 |\n")
	output.WriteString("|---|---|---:|---|\n")
	for _, probe := range report.Probes {
		for _, check := range probe.Checks {
			fmt.Fprintf(&output, "| `%s` | `%s` | %s | %s |\n",
				probe.ID,
				check.ID,
				passFailZH(check.Passed),
				tableText(check.Detail),
			)
		}
	}

	output.WriteString("\n## 发布门禁\n\n")
	if report.Gate.PublicationReady {
		output.WriteString("全部发布必需探针已通过，`publication_ready=true`。\n")
	} else {
		output.WriteString("当前报告不能作为生产发布准入证据，`publication_ready=false`。\n\n")
		if len(report.Gate.BlockingProbes) > 0 {
			output.WriteString("- 缺少执行入口的探针：")
			output.WriteString(codeList(report.Gate.BlockingProbes))
			output.WriteString("\n")
		}
		if len(report.Gate.FailedProbes) > 0 {
			output.WriteString("- 执行失败的探针：")
			output.WriteString(codeList(report.Gate.FailedProbes))
			output.WriteString("\n")
		}
	}
	output.WriteString("\n`blocked` 表示仓库中没有对应生产入口或本次未提供命令。")
	output.WriteString("报告不会用测试替身补写容量、故障恢复或越权结论。\n")
	return output.String()
}

func renderEnglishReport(report acceptanceReport) string {
	var output strings.Builder
	output.WriteString("# City delivery production acceptance report\n\n")
	output.WriteString("> `go run ./scripts/delivery-acceptance report` generates this file. ")
	output.WriteString("The authoritative data is [`delivery-acceptance.v1.json`](./delivery-acceptance.v1.json).\n\n")
	output.WriteString("## Runtime environment\n\n")
	fmt.Fprintf(&output, "- Generated at: `%s`\n", report.GeneratedAt.Format("2006-01-02T15:04:05Z"))
	fmt.Fprintf(&output, "- Source revision: `%s`\n", report.SourceRevision)
	fmt.Fprintf(&output, "- Acceptance tool SHA-256: `%s`\n", report.ToolSHA256)
	fmt.Fprintf(&output, "- Tested source SHA-256: `%s`\n", report.SubjectSHA256)
	fmt.Fprintf(&output, "- Go: `%s`; platform: `%s/%s`\n",
		report.Environment.GoVersion, report.Environment.GOOS, report.Environment.GOARCH)
	fmt.Fprintf(&output, "- Operating system: `%s`\n", report.Environment.OSVersion)
	fmt.Fprintf(&output, "- CPU: `%s`; memory: `%d` bytes\n\n",
		report.Environment.CPUModel, report.Environment.MemoryBytes)

	output.WriteString("## Probe status\n\n")
	output.WriteString("| Probe | Status | Required for publication | Detail |\n")
	output.WriteString("|---|---|---:|---|\n")
	for _, probe := range report.Probes {
		fmt.Fprintf(&output, "| `%s` | `%s` | %s | %s |\n",
			probe.ID,
			probe.Status,
			yesNoEN(probe.RequiredForPublication),
			tableText(probe.Reason),
		)
	}

	output.WriteString("\n## Capacity and recovery measurements\n\n")
	output.WriteString("| Probe | Scenario | Samples | Tasks/vehicles/depots/cargo per vehicle | Queries/SSE | p50 | p95 | p99 | Peak memory | Feasibility | Quality gap | Recovery |\n")
	output.WriteString("|---|---|---:|---|---|---:|---:|---:|---:|---:|---:|---:|\n")
	writeMeasurementRows(&output, report)

	output.WriteString("\n## Check results\n\n")
	output.WriteString("| Probe | Check | Result | Evidence |\n")
	output.WriteString("|---|---|---:|---|\n")
	for _, probe := range report.Probes {
		for _, check := range probe.Checks {
			fmt.Fprintf(&output, "| `%s` | `%s` | %s | %s |\n",
				probe.ID,
				check.ID,
				passFailEN(check.Passed),
				tableText(check.Detail),
			)
		}
	}

	output.WriteString("\n## Publication gate\n\n")
	if report.Gate.PublicationReady {
		output.WriteString("Every required probe passed. `publication_ready=true`.\n")
	} else {
		output.WriteString("This report does not admit a production release. `publication_ready=false`.\n\n")
		if len(report.Gate.BlockingProbes) > 0 {
			output.WriteString("- Probes without an execution entry point: ")
			output.WriteString(codeList(report.Gate.BlockingProbes))
			output.WriteString("\n")
		}
		if len(report.Gate.FailedProbes) > 0 {
			output.WriteString("- Failed probes: ")
			output.WriteString(codeList(report.Gate.FailedProbes))
			output.WriteString("\n")
		}
	}
	output.WriteString("\n`blocked` means that the repository has no corresponding production entry point ")
	output.WriteString("or that this run did not provide its command. The report does not replace ")
	output.WriteString("capacity, recovery, or authorization evidence with a test double.\n")
	return output.String()
}

func writeMeasurementRows(output *strings.Builder, report acceptanceReport) {
	rows := 0
	for _, probe := range report.Probes {
		for _, measurement := range probe.Measurements {
			rows++
			fmt.Fprintf(
				output,
				"| `%s` | `%s` | %d | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
				probe.ID,
				measurement.ID,
				measurement.SampleCount,
				dimensions(measurement),
				concurrency(measurement),
				formatDuration(measurement.DurationNS.P50),
				formatDuration(measurement.DurationNS.P95),
				formatDuration(measurement.DurationNS.P99),
				formatBytes(measurement.PeakMemoryBytes),
				formatPPM(measurement.FeasibilityPPM),
				formatPPM(measurement.QualityGapPPM),
				formatOptionalDuration(measurement.RecoveryNS),
			)
		}
	}
	if rows == 0 {
		output.WriteString("| - | - | 0 | - | - | - | - | - | - | - | - | - |\n")
	}
}

func dimensions(value measurementEvidence) string {
	if value.TaskCount == 0 &&
		value.VehicleCount == 0 &&
		value.DepotCount == 0 &&
		value.CargoPerVehicle == 0 {
		return "-"
	}
	return fmt.Sprintf(
		"%d/%d/%d/%d",
		value.TaskCount,
		value.VehicleCount,
		value.DepotCount,
		value.CargoPerVehicle,
	)
}

func concurrency(value measurementEvidence) string {
	if value.QueryConcurrency == 0 && value.SSEConnections == 0 {
		return "-"
	}
	return fmt.Sprintf("%d/%d", value.QueryConcurrency, value.SSEConnections)
}

func formatDuration(value int64) string {
	if value <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.3f ms", float64(value)/1_000_000)
}

func formatOptionalDuration(value int64) string {
	if value == 0 {
		return "-"
	}
	return formatDuration(value)
}

func formatBytes(value uint64) string {
	if value == 0 {
		return "-"
	}
	return fmt.Sprintf("%.2f MiB", float64(value)/(1024*1024))
}

func formatPPM(value int64) string {
	if value == 0 {
		return "0 ppm"
	}
	return fmt.Sprintf("%d ppm", value)
}

func tableText(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

func yesNoZH(value bool) string {
	if value {
		return "是"
	}
	return "否"
}

func yesNoEN(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func passFailZH(value bool) string {
	if value {
		return "通过"
	}
	return "失败"
}

func passFailEN(value bool) string {
	if value {
		return "pass"
	}
	return "fail"
}

func codeList(values []string) string {
	if len(values) == 0 {
		return "无"
	}
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = "`" + value + "`"
	}
	return strings.Join(quoted, "、")
}
