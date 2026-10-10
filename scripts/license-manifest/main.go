package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

type packageRecord struct {
	ImportPath string
	Module     *moduleRecord
}

type moduleRecord struct {
	Path string
	Main bool
}

type licenseRecord struct {
	Package string
	URL     string
	Type    string
}

type moduleLicense struct {
	Module string
	URL    string
	Type   string
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("license-manifest", flag.ContinueOnError)
	goLicenses := flags.String("go-licenses", "", "path to the go-licenses executable")
	output := flags.String("output", "", "output CSV path")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *goLicenses == "" || *output == "" {
		fmt.Fprintln(os.Stderr, "--go-licenses and --output are required")
		return 2
	}
	root, err := repositoryRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	graph, err := loadTargetGraph(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	records, err := runGoLicenses(root, *goLicenses, []string{"./..."})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	licenses := assignLicenses(graph, records)
	for _, module := range sortedMissingModules(graph, licenses) {
		found := false
		for _, candidate := range graph[module] {
			candidateRecords, commandErr := runGoLicenses(
				root,
				*goLicenses,
				[]string{candidate},
			)
			if commandErr != nil {
				continue
			}
			for key, value := range assignLicenses(graph, candidateRecords) {
				licenses[key] = value
			}
			if hasModuleLicense(licenses, module) {
				found = true
				break
			}
		}
		if !found {
			fmt.Fprintf(os.Stderr, "no license found for target module %s\n", module)
			return 1
		}
	}
	if err := writeManifest(*output, licenses); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func loadTargetGraph(root string) (map[string][]string, error) {
	command := exec.Command("go", "list", "-deps", "-json", "./...")
	command.Dir = root
	command.Env = append(
		os.Environ(),
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH=amd64",
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	graph := make(map[string][]string)
	decoder := json.NewDecoder(stdout)
	for {
		var record packageRecord
		err := decoder.Decode(&record)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = command.Wait()
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		if record.Module == nil || record.Module.Main || record.Module.Path == "" {
			continue
		}
		graph[record.Module.Path] = appendUnique(
			graph[record.Module.Path],
			record.ImportPath,
		)
	}
	if err := command.Wait(); err != nil {
		return nil, fmt.Errorf("go list target graph: %s", strings.TrimSpace(stderr.String()))
	}
	for module := range graph {
		slices.SortFunc(graph[module], func(left string, right string) int {
			if len(left) != len(right) {
				return len(left) - len(right)
			}
			return strings.Compare(left, right)
		})
	}
	return graph, nil
}

func runGoLicenses(
	root string,
	executable string,
	patterns []string,
) ([]licenseRecord, error) {
	args := append([]string{"report"}, patterns...)
	command := exec.Command(executable, args...)
	command.Dir = root
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf(
			"go-licenses report %s: %s",
			strings.Join(patterns, " "),
			strings.TrimSpace(stderr.String()),
		)
	}
	reader := csv.NewReader(bytes.NewReader(stdout.Bytes()))
	var records []licenseRecord
	for {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse go-licenses CSV: %w", err)
		}
		if len(row) != 3 {
			return nil, fmt.Errorf("go-licenses row has %d fields", len(row))
		}
		records = append(records, licenseRecord{
			Package: row[0],
			URL:     row[1],
			Type:    row[2],
		})
	}
	return records, nil
}

func assignLicenses(
	graph map[string][]string,
	records []licenseRecord,
) map[string]moduleLicense {
	modules := make([]string, 0, len(graph))
	for module := range graph {
		modules = append(modules, module)
	}
	slices.SortFunc(modules, func(left string, right string) int {
		if len(left) != len(right) {
			return len(right) - len(left)
		}
		return strings.Compare(left, right)
	})
	result := make(map[string]moduleLicense)
	for _, record := range records {
		for _, module := range modules {
			if record.Package != module &&
				!strings.HasPrefix(record.Package, module+"/") {
				continue
			}
			value := moduleLicense{Module: module, URL: record.URL, Type: record.Type}
			result[module+"\x00"+record.URL+"\x00"+record.Type] = value
			break
		}
	}
	return result
}

func sortedMissingModules(
	graph map[string][]string,
	licenses map[string]moduleLicense,
) []string {
	missing := make([]string, 0)
	for module := range graph {
		if !hasModuleLicense(licenses, module) {
			missing = append(missing, module)
		}
	}
	slices.Sort(missing)
	return missing
}

func hasModuleLicense(licenses map[string]moduleLicense, module string) bool {
	for _, value := range licenses {
		if value.Module == module {
			return true
		}
	}
	return false
}

func writeManifest(path string, licenses map[string]moduleLicense) error {
	values := make([]moduleLicense, 0, len(licenses))
	for _, value := range licenses {
		values = append(values, value)
	}
	slices.SortFunc(values, func(left moduleLicense, right moduleLicense) int {
		if order := strings.Compare(left.Module, right.Module); order != 0 {
			return order
		}
		if order := strings.Compare(left.URL, right.URL); order != 0 {
			return order
		}
		return strings.Compare(left.Type, right.Type)
	})
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	writer := csv.NewWriter(file)
	for _, value := range values {
		if err := writer.Write([]string{value.Module, value.URL, value.Type}); err != nil {
			_ = file.Close()
			return err
		}
	}
	writer.Flush()
	return errors.Join(writer.Error(), file.Close())
}

func appendUnique(values []string, candidate string) []string {
	if candidate == "" || slices.Contains(values, candidate) {
		return values
	}
	return append(values, candidate)
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
