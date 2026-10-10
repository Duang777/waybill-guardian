package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultConfigPath  = "scripts/delivery-acceptance/config.json"
	defaultReportPath  = "docs/reports/delivery-acceptance.v1.json"
	defaultChinesePath = "docs/reports/delivery-acceptance.md"
	defaultEnglishPath = "docs/reports/delivery-acceptance.en.md"
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
	case "run":
		commandErr = runEvidence(ctx, root, args[1:])
	case "report":
		commandErr = runReport(root, args[1:])
	case "check":
		commandErr = runCheck(root, args[1:])
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

func runEvidence(ctx context.Context, root string, args []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	configPath := flags.String("config", defaultConfigPath, "suite configuration")
	reportPath := flags.String("report", defaultReportPath, "authoritative JSON report")
	chinesePath := flags.String("markdown-zh", defaultChinesePath, "generated Chinese report")
	englishPath := flags.String("markdown-en", defaultEnglishPath, "generated English report")
	skipBrowser := flags.Bool("skip-browser", false, "record the browser probe as blocked")
	requirePublication := flags.Bool(
		"require-publication",
		false,
		"fail unless every required probe passes",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := rejectPositionalArguments(flags); err != nil {
		return err
	}
	absoluteConfigPath := resolve(root, *configPath)
	config, configRaw, err := loadConfig(absoluteConfigPath)
	if err != nil {
		return err
	}
	report := runAcceptance(
		ctx,
		root,
		absoluteConfigPath,
		config,
		configRaw,
		*skipBrowser,
	)
	if err := verifyReport(root, config, configRaw, report); err != nil {
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
			"publication gate is blocked by %v and failed on %v",
			report.Gate.BlockingProbes,
			report.Gate.FailedProbes,
		)
	}
	fmt.Printf(
		"wrote %s; passed=%d blocked=%d failed=%d publication_ready=%t\n",
		filepath.ToSlash(*reportPath),
		len(report.Gate.PassedProbes),
		len(report.Gate.BlockingProbes),
		len(report.Gate.FailedProbes),
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
	if err := rejectPositionalArguments(flags); err != nil {
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
	fmt.Println("regenerated acceptance Markdown from authoritative JSON")
	return nil
}

func runCheck(root string, args []string) error {
	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	configPath := flags.String("config", defaultConfigPath, "suite configuration")
	reportPath := flags.String("report", defaultReportPath, "authoritative JSON report")
	chinesePath := flags.String("markdown-zh", defaultChinesePath, "generated Chinese report")
	englishPath := flags.String("markdown-en", defaultEnglishPath, "generated English report")
	requirePublication := flags.Bool(
		"require-publication",
		false,
		"fail unless every required probe passes",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := rejectPositionalArguments(flags); err != nil {
		return err
	}
	config, configRaw, err := loadConfig(resolve(root, *configPath))
	if err != nil {
		return err
	}
	report, _, err := loadReport(resolve(root, *reportPath))
	if err != nil {
		return err
	}
	if err := verifyReport(root, config, configRaw, report); err != nil {
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
		return fmt.Errorf(
			"publication gate is blocked by %v and failed on %v",
			report.Gate.BlockingProbes,
			report.Gate.FailedProbes,
		)
	}
	fmt.Printf(
		"acceptance evidence and generated Markdown are valid; publication_ready=%t\n",
		report.Gate.PublicationReady,
	)
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

func resolve(root string, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, filepath.FromSlash(path))
}

func rejectPositionalArguments(flags *flag.FlagSet) error {
	if flags.NArg() == 0 {
		return nil
	}
	return fmt.Errorf(
		"%s: unexpected positional arguments: %s",
		flags.Name(),
		strings.Join(flags.Args(), " "),
	)
}

func writeAtomic(path string, raw []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".delivery-acceptance-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(mode); err != nil {
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
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	remove = false
	return nil
}

func printUsage(output *os.File) {
	fmt.Fprintln(
		output,
		"usage: go run ./scripts/delivery-acceptance <run|report|check> [flags]",
	)
}
