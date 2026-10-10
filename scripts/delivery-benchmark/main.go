package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

const (
	defaultConfigPath   = "scripts/delivery-benchmark/config.json"
	defaultManifestPath = "data/simulated/delivery-benchmark-v1/manifest.json"
	defaultReportPath   = "docs/reports/delivery-benchmark.v1.json"
	defaultChinesePath  = "docs/reports/delivery-benchmark.md"
	defaultEnglishPath  = "docs/reports/delivery-benchmark.en.md"
)

func main() {
	os.Exit(runCLI(context.Background(), os.Args[1:]))
}

func runCLI(ctx context.Context, args []string) int {
	if len(args) == 0 {
		printUsage(os.Stderr)
		return 2
	}
	root, err := repositoryRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var commandErr error
	switch args[0] {
	case "generate":
		commandErr = runGenerate(root, args[1:])
	case "run":
		commandErr = runEvidence(ctx, root, args[1:])
	case "report":
		commandErr = runReport(root, args[1:])
	case "check":
		commandErr = runCheck(ctx, root, args[1:])
	case "docs-check":
		commandErr = runDocsCheck(ctx, root, args[1:])
	default:
		printUsage(os.Stderr)
		return 2
	}
	if commandErr != nil {
		fmt.Fprintln(os.Stderr, commandErr)
		return 1
	}
	return 0
}

func runGenerate(root string, args []string) error {
	flags := flag.NewFlagSet("generate", flag.ContinueOnError)
	configPath := flags.String("config", defaultConfigPath, "suite configuration")
	manifestPath := flags.String("manifest", defaultManifestPath, "dataset manifest")
	outputDir := flags.String("output-dir", "", "optional directory for canonical problem JSON")
	write := flags.Bool("write", false, "write the manifest")
	if err := flags.Parse(args); err != nil {
		return err
	}
	config, _, err := loadConfig(resolve(root, *configPath))
	if err != nil {
		return err
	}
	datasets, manifest, err := generateDatasets(config)
	if err != nil {
		return err
	}
	raw, err := marshalIndented(manifest)
	if err != nil {
		return err
	}
	manifestFile := resolve(root, *manifestPath)
	if *write {
		if err := writeAtomic(manifestFile, raw, 0o644); err != nil {
			return fmt.Errorf("write manifest: %w", err)
		}
	} else if err := compareFile(manifestFile, raw, "dataset manifest"); err != nil {
		return err
	}
	if *outputDir != "" {
		target := resolve(root, *outputDir)
		for _, dataset := range datasets {
			path := filepath.Join(target, dataset.spec.ID+".problem.json")
			if err := writeAtomic(path, append(dataset.canonical, '\n'), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", dataset.spec.ID, err)
			}
		}
	}
	fmt.Printf(
		"verified %d deterministic datasets from %s\n",
		len(datasets),
		filepath.ToSlash(*configPath),
	)
	return nil
}

func runEvidence(ctx context.Context, root string, args []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	configPath := flags.String("config", defaultConfigPath, "suite configuration")
	manifestPath := flags.String("manifest", defaultManifestPath, "dataset manifest")
	reportPath := flags.String("report", defaultReportPath, "authoritative JSON report")
	chinesePath := flags.String("markdown-zh", defaultChinesePath, "generated Chinese report")
	englishPath := flags.String("markdown-en", defaultEnglishPath, "generated English report")
	requirePublication := flags.Bool(
		"require-publication",
		false,
		"fail unless every required adapter passes",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	config, configRaw, err := loadConfig(resolve(root, *configPath))
	if err != nil {
		return err
	}
	datasets, generatedManifest, err := generateDatasets(config)
	if err != nil {
		return err
	}
	generatedManifestRaw, err := marshalIndented(generatedManifest)
	if err != nil {
		return err
	}
	manifest, manifestRaw, err := loadManifest(resolve(root, *manifestPath))
	if err != nil {
		return err
	}
	if !bytes.Equal(generatedManifestRaw, manifestRaw) {
		return fmt.Errorf(
			"dataset manifest is stale; run go run ./scripts/delivery-benchmark generate --write",
		)
	}
	report := runBenchmark(
		ctx,
		root,
		config,
		configRaw,
		datasets,
		manifest,
		manifestRaw,
	)
	if err := verifyReport(root, report, config, configRaw, manifest, manifestRaw); err != nil {
		return fmt.Errorf("verify generated report: %w", err)
	}
	reportRaw, err := marshalIndented(report)
	if err != nil {
		return err
	}
	chinese, err := renderReport(report, "zh")
	if err != nil {
		return err
	}
	english, err := renderReport(report, "en")
	if err != nil {
		return err
	}
	if err := writeAtomic(resolve(root, *reportPath), reportRaw, 0o644); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	if err := writeAtomic(resolve(root, *chinesePath), []byte(chinese), 0o644); err != nil {
		return fmt.Errorf("write Chinese report: %w", err)
	}
	if err := writeAtomic(resolve(root, *englishPath), []byte(english), 0o644); err != nil {
		return fmt.Errorf("write English report: %w", err)
	}
	if *requirePublication && !report.Gate.PublicationReady {
		return fmt.Errorf(
			"publication gate is blocked by %v",
			report.Gate.BlockingAdapters,
		)
	}
	fmt.Printf(
		"wrote %s with %d replays per adapter and dataset; publication_ready=%t\n",
		filepath.ToSlash(*reportPath),
		report.ReplayCount,
		report.Gate.PublicationReady,
	)
	return nil
}

func runReport(root string, args []string) error {
	flags := flag.NewFlagSet("report", flag.ContinueOnError)
	reportPath := flags.String("report", defaultReportPath, "authoritative JSON report")
	chinesePath := flags.String("markdown-zh", defaultChinesePath, "generated Chinese report")
	englishPath := flags.String("markdown-en", defaultEnglishPath, "generated English report")
	if err := flags.Parse(args); err != nil {
		return err
	}
	report, _, err := loadReport(resolve(root, *reportPath))
	if err != nil {
		return err
	}
	chinese, err := renderReport(report, "zh")
	if err != nil {
		return err
	}
	english, err := renderReport(report, "en")
	if err != nil {
		return err
	}
	if err := writeAtomic(resolve(root, *chinesePath), []byte(chinese), 0o644); err != nil {
		return err
	}
	if err := writeAtomic(resolve(root, *englishPath), []byte(english), 0o644); err != nil {
		return err
	}
	fmt.Println("regenerated benchmark Markdown from authoritative JSON")
	return nil
}

func runCheck(ctx context.Context, root string, args []string) error {
	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	configPath := flags.String("config", defaultConfigPath, "suite configuration")
	manifestPath := flags.String("manifest", defaultManifestPath, "dataset manifest")
	reportPath := flags.String("report", defaultReportPath, "authoritative JSON report")
	chinesePath := flags.String("markdown-zh", defaultChinesePath, "generated Chinese report")
	englishPath := flags.String("markdown-en", defaultEnglishPath, "generated English report")
	external := flags.Bool("external", false, "check external HTTP links")
	requirePublication := flags.Bool(
		"require-publication",
		false,
		"fail unless every required adapter passes",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	config, configRaw, err := loadConfig(resolve(root, *configPath))
	if err != nil {
		return err
	}
	_, generatedManifest, err := generateDatasets(config)
	if err != nil {
		return err
	}
	generatedManifestRaw, err := marshalIndented(generatedManifest)
	if err != nil {
		return err
	}
	manifest, manifestRaw, err := loadManifest(resolve(root, *manifestPath))
	if err != nil {
		return err
	}
	if !bytes.Equal(generatedManifestRaw, manifestRaw) {
		return fmt.Errorf("dataset manifest is not reproducible")
	}
	report, _, err := loadReport(resolve(root, *reportPath))
	if err != nil {
		return err
	}
	if err := verifyReport(root, report, config, configRaw, manifest, manifestRaw); err != nil {
		return err
	}
	chinese, _ := renderReport(report, "zh")
	english, _ := renderReport(report, "en")
	if err := compareFile(resolve(root, *chinesePath), []byte(chinese), "Chinese report"); err != nil {
		return err
	}
	if err := compareFile(resolve(root, *englishPath), []byte(english), "English report"); err != nil {
		return err
	}
	if *requirePublication && !report.Gate.PublicationReady {
		return fmt.Errorf("publication gate is blocked by %v", report.Gate.BlockingAdapters)
	}
	if err := checkDocumentation(ctx, root, *external); err != nil {
		return err
	}
	fmt.Printf(
		"benchmark evidence, generated Markdown, and documentation links are valid; external=%t\n",
		*external,
	)
	return nil
}

func runDocsCheck(ctx context.Context, root string, args []string) error {
	flags := flag.NewFlagSet("docs-check", flag.ContinueOnError)
	external := flags.Bool("external", false, "check external HTTP links")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := checkDocumentation(ctx, root, *external); err != nil {
		return err
	}
	fmt.Printf("documentation links and test references are valid; external=%t\n", *external)
	return nil
}

func repositoryRoot() (string, error) {
	current, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("go.mod was not found above the current directory")
		}
		current = parent
	}
}

func resolve(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, filepath.FromSlash(path))
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

func printUsage(output *os.File) {
	fmt.Fprintln(
		output,
		"usage: go run ./scripts/delivery-benchmark <generate|run|report|check|docs-check> [flags]",
	)
}
