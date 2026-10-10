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
	"strconv"
	"strings"
	"time"
)

func runCommandProbe(
	ctx context.Context,
	root string,
	configPath string,
	timeout time.Duration,
	spec probeSpec,
) probeEvidence {
	evidence := probeEvidence{
		ID:                     spec.ID,
		Label:                  spec.Label,
		Kind:                   spec.Kind,
		RequiredForPublication: spec.RequiredForPublication,
		Status:                 statusBlocked,
		CommandSource:          spec.CommandEnv,
		Checks:                 []checkEvidence{},
		Measurements:           []measurementEvidence{},
		Artifacts:              []artifactEvidence{},
	}
	rawCommand := strings.TrimSpace(os.Getenv(spec.CommandEnv))
	if rawCommand == "" {
		evidence.Reason = "command environment variable " + spec.CommandEnv + " is not set"
		return evidence
	}
	var command []string
	if err := decodeStrict([]byte(rawCommand), &command); err != nil {
		evidence.Status = statusFailed
		evidence.Reason = "command environment variable is not a JSON string array"
		return evidence
	}
	if err := validateAcceptanceCommand(command); err != nil {
		evidence.Status = statusFailed
		evidence.Reason = err.Error()
		return evidence
	}
	workDir, err := os.MkdirTemp("", "delivery-acceptance-"+spec.ID+"-")
	if err != nil {
		evidence.Status = statusFailed
		evidence.Reason = "create command work directory"
		return evidence
	}
	defer os.RemoveAll(workDir)
	resultPath := filepath.Join(workDir, "result.json")
	expanded := expandCommand(command, map[string]string{
		"{config}": configPath,
		"{result}": resultPath,
		"{root}":   root,
		"{probe}":  spec.ID,
	})
	runContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	process := exec.CommandContext(runContext, expanded[0], expanded[1:]...)
	process.Dir = root
	process.Stdout = &boundedBuffer{limit: 256 * 1024}
	process.Stderr = &boundedBuffer{limit: 256 * 1024}
	if err := process.Run(); err != nil {
		evidence.Status = statusFailed
		if errors.Is(runContext.Err(), context.DeadlineExceeded) {
			evidence.Reason = fmt.Sprintf("command exceeded %s timeout", timeout)
		} else {
			evidence.Reason = "command failed: " + publicProcessError(err)
		}
		return evidence
	}
	rawResult, err := os.ReadFile(resultPath)
	if err != nil {
		evidence.Status = statusFailed
		evidence.Reason = "command did not write {result}"
		return evidence
	}
	var result commandResult
	if err := decodeStrict(rawResult, &result); err != nil {
		evidence.Status = statusFailed
		evidence.Reason = "command result is not strict JSON: " + err.Error()
		return evidence
	}
	if err := validateCommandResult(spec, result); err != nil {
		evidence.Status = statusFailed
		evidence.Reason = err.Error()
		return evidence
	}
	evidence.Status = statusPassed
	evidence.Checks = result.Checks
	evidence.Measurements = result.Measurements
	evidence.Artifacts = result.Artifacts
	return evidence
}

func validateAcceptanceCommand(command []string) error {
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return fmt.Errorf("acceptance command array is empty")
	}
	hasResult := false
	for _, argument := range command {
		if strings.ContainsRune(argument, '\x00') {
			return fmt.Errorf("acceptance command contains a NUL byte")
		}
		if strings.Contains(argument, "{result}") {
			hasResult = true
		}
	}
	if !hasResult {
		return fmt.Errorf("acceptance command must contain a {result} placeholder")
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
	limit int
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	remaining := buffer.limit - buffer.value.Len()
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
		return "exit code " + strconv.Itoa(exitError.ExitCode())
	}
	return "process could not start"
}

type browserResult struct {
	OK           bool                 `json:"ok"`
	Canvas       browserCanvas        `json:"canvas"`
	LargeCanvas  browserCanvas        `json:"largeCanvas"`
	StateMatrix  []string             `json:"stateMatrix"`
	MountSamples []browserMountSample `json:"mountSamples"`
	Desktop      string               `json:"desktop"`
	Mobile       string               `json:"mobile"`
	Capacity     string               `json:"capacity"`
}

type browserCanvas struct {
	Width           int    `json:"width"`
	Height          int    `json:"height"`
	Colors          int    `json:"colors"`
	ChromaticPixels int    `json:"chromaticPixels"`
	Signature       uint32 `json:"signature"`
}

type browserMountSample struct {
	Documents        int `json:"documents"`
	Nodes            int `json:"nodes"`
	JSEventListeners int `json:"jsEventListeners"`
}

func runBrowserProbe(
	ctx context.Context,
	root string,
	spec probeSpec,
	config browserConfig,
	skip bool,
) probeEvidence {
	evidence := probeEvidence{
		ID:                     spec.ID,
		Label:                  spec.Label,
		Kind:                   spec.Kind,
		RequiredForPublication: spec.RequiredForPublication,
		Status:                 statusBlocked,
		Checks:                 []checkEvidence{},
		Measurements:           []measurementEvidence{},
		Artifacts:              []artifactEvidence{},
	}
	if skip {
		evidence.Reason = "browser probe was skipped by command line"
		return evidence
	}
	runContext, cancel := context.WithTimeout(
		ctx,
		time.Duration(config.TimeoutSeconds)*time.Second,
	)
	defer cancel()
	process := exec.CommandContext(runContext, "npm", "run", "verify:delivery")
	process.Dir = filepath.Join(root, "web")
	stdout := &boundedBuffer{limit: 4 * 1024 * 1024}
	stderr := &boundedBuffer{limit: 512 * 1024}
	process.Stdout = stdout
	process.Stderr = stderr
	if err := process.Run(); err != nil {
		evidence.Status = statusFailed
		if errors.Is(runContext.Err(), context.DeadlineExceeded) {
			evidence.Reason = fmt.Sprintf(
				"browser probe exceeded %d second timeout",
				config.TimeoutSeconds,
			)
		} else {
			evidence.Reason = "browser probe failed: " + publicProcessError(err)
		}
		return evidence
	}
	result, err := parseBrowserResult(stdout.value.Bytes())
	if err != nil {
		evidence.Status = statusFailed
		evidence.Reason = err.Error()
		return evidence
	}
	checks := []checkEvidence{
		check("cargo_count", result.OK,
			fmt.Sprintf("verify:delivery completed its %d-item assertion", config.CargoItems)),
		check("instanced_rendering", result.LargeCanvas.Width > 0,
			"verify:delivery completed its InstancedMesh assertion"),
		check(
			"nonblank_canvas",
			result.LargeCanvas.Colors >= 8 && result.LargeCanvas.ChromaticPixels >= 3,
			fmt.Sprintf("colors=%d chromatic_pixels=%d",
				result.LargeCanvas.Colors, result.LargeCanvas.ChromaticPixels),
		),
		check("state_matrix", len(result.StateMatrix) == config.StateCount,
			fmt.Sprintf("states=%d expected=%d", len(result.StateMatrix), config.StateCount)),
		check("remount_release", len(result.MountSamples) == config.RemountCycles+1,
			fmt.Sprintf("samples=%d remount_cycles=%d",
				len(result.MountSamples), config.RemountCycles)),
	}
	commandShape := commandResult{
		SchemaVersion: commandResultSchemaVersion,
		ProbeID:       spec.ID,
		Checks:        checks,
	}
	if err := validateEmbeddedChecks(spec, commandShape); err != nil {
		evidence.Status = statusFailed
		evidence.Reason = err.Error()
		return evidence
	}
	artifacts, err := hashBrowserArtifacts(root, result)
	if err != nil {
		evidence.Status = statusFailed
		evidence.Reason = err.Error()
		return evidence
	}
	evidence.Status = statusPassed
	evidence.Checks = checks
	evidence.Artifacts = artifacts
	return evidence
}

func parseBrowserResult(raw []byte) (browserResult, error) {
	marker := bytes.LastIndex(raw, []byte("\n{"))
	if marker < 0 {
		return browserResult{}, fmt.Errorf("browser probe did not emit its JSON result")
	}
	var result browserResult
	if err := decodeStrict(raw[marker+1:], &result); err != nil {
		return browserResult{}, fmt.Errorf("decode browser result: %w", err)
	}
	if !result.OK {
		return browserResult{}, fmt.Errorf("browser probe reported ok=false")
	}
	return result, nil
}

func hashBrowserArtifacts(
	root string,
	result browserResult,
) ([]artifactEvidence, error) {
	paths := []string{result.Desktop, result.Mobile, result.Capacity}
	output := make([]artifactEvidence, 0, len(paths))
	for _, path := range paths {
		absolute := path
		if !filepath.IsAbs(absolute) {
			absolute = filepath.Join(root, path)
		}
		raw, err := os.ReadFile(absolute)
		if err != nil {
			return nil, fmt.Errorf("read browser artifact %s: %w", path, err)
		}
		sum := sha256.Sum256(raw)
		output = append(output, artifactEvidence{
			Name:   filepath.Base(path),
			SHA256: hex.EncodeToString(sum[:]),
			Bytes:  int64(len(raw)),
		})
	}
	return output, nil
}

func writeCommandResult(path string, result commandResult) error {
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o600)
}
