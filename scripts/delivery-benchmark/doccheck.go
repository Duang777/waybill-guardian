package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

var (
	markdownLinkPattern = regexp.MustCompile(`!?\[[^\]]*\]\(([^)\s]+)(?:\s+["'][^"']*["'])?\)`)
	htmlLinkPattern     = regexp.MustCompile(`(?i)(?:href|src|srcset)=["']([^"']+)["']`)
	autoLinkPattern     = regexp.MustCompile(`<((?:https?://)[^>]+)>`)
	headingPattern      = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)
	htmlIDPattern       = regexp.MustCompile(`(?i)<(?:a|h[1-6])[^>]+id=["']([^"']+)["']`)
	testNamePattern     = regexp.MustCompile(`\bTest[A-Za-z0-9_]+\b`)
	testFunctionPattern = regexp.MustCompile(`\bfunc\s+(Test[A-Za-z0-9_]+)\s*\(`)
	htmlTagPattern      = regexp.MustCompile(`<[^>]+>`)
)

type documentLink struct {
	source string
	target string
	line   int
}

func checkDocumentation(ctx context.Context, root string, external bool) error {
	documents, err := documentationFiles(root)
	if err != nil {
		return err
	}
	links := make([]documentLink, 0)
	for _, path := range documents {
		current, err := extractDocumentLinks(path)
		if err != nil {
			return err
		}
		links = append(links, current...)
	}
	failures := checkLocalLinks(root, links)
	failures = append(failures, checkPublicTestReferences(root)...)
	if external {
		failures = append(failures, checkExternalLinks(ctx, links)...)
	}
	if len(failures) > 0 {
		slices.Sort(failures)
		return fmt.Errorf("documentation check failed:\n%s", strings.Join(failures, "\n"))
	}
	return nil
}

func documentationFiles(root string) ([]string, error) {
	result := []string{
		filepath.Join(root, "AGENTS.md"),
		filepath.Join(root, "README.md"),
		filepath.Join(root, "README.en.md"),
		filepath.Join(root, "THIRD_PARTY_NOTICES.md"),
		filepath.Join(root, "web", "AGENTS.md"),
	}
	docsRoot := filepath.Join(root, "docs")
	err := filepath.WalkDir(docsRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".md") {
			result = append(result, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}

func extractDocumentLinks(path string) ([]documentLink, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	var result []documentLink
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	inCodeFence := false
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inCodeFence = !inCodeFence
			continue
		}
		if inCodeFence {
			continue
		}
		for _, match := range markdownLinkPattern.FindAllStringSubmatch(line, -1) {
			result = append(result, documentLink{source: path, target: match[1], line: lineNumber})
		}
		for _, match := range htmlLinkPattern.FindAllStringSubmatch(line, -1) {
			for _, target := range strings.Fields(match[1]) {
				result = append(result, documentLink{source: path, target: target, line: lineNumber})
			}
		}
		for _, match := range autoLinkPattern.FindAllStringSubmatch(line, -1) {
			result = append(result, documentLink{source: path, target: match[1], line: lineNumber})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	return result, nil
}

func checkLocalLinks(root string, links []documentLink) []string {
	anchorCache := make(map[string]map[string]struct{})
	var failures []string
	for _, link := range links {
		target := strings.TrimSpace(link.target)
		if target == "" || strings.HasPrefix(target, "mailto:") ||
			strings.HasPrefix(target, "data:") ||
			strings.HasPrefix(target, "http://") ||
			strings.HasPrefix(target, "https://") {
			continue
		}
		if strings.HasPrefix(target, "/") && !filepath.IsAbs(target) {
			failures = append(failures, formatLinkFailure(root, link, "repository-relative link must not start with /"))
			continue
		}
		pathPart, anchor := splitTarget(target)
		targetPath := link.source
		if pathPart != "" {
			decoded, err := url.PathUnescape(pathPart)
			if err != nil {
				failures = append(failures, formatLinkFailure(root, link, "invalid URL encoding"))
				continue
			}
			targetPath = filepath.Clean(filepath.Join(filepath.Dir(link.source), filepath.FromSlash(decoded)))
		}
		info, err := os.Stat(targetPath)
		if err != nil {
			failures = append(failures, formatLinkFailure(root, link, "target does not exist"))
			continue
		}
		if anchor == "" || info.IsDir() {
			continue
		}
		if !strings.EqualFold(filepath.Ext(targetPath), ".md") {
			failures = append(failures, formatLinkFailure(root, link, "anchor target is not Markdown"))
			continue
		}
		anchors, exists := anchorCache[targetPath]
		if !exists {
			anchors, err = markdownAnchors(targetPath)
			if err != nil {
				failures = append(failures, formatLinkFailure(root, link, err.Error()))
				continue
			}
			anchorCache[targetPath] = anchors
		}
		decodedAnchor, err := url.PathUnescape(anchor)
		if err != nil {
			failures = append(failures, formatLinkFailure(root, link, "invalid anchor encoding"))
			continue
		}
		if _, exists := anchors[strings.ToLower(decodedAnchor)]; !exists {
			failures = append(failures, formatLinkFailure(root, link, "anchor does not exist"))
		}
	}
	return failures
}

func splitTarget(target string) (string, string) {
	parts := strings.SplitN(target, "#", 2)
	if len(parts) == 1 {
		return strings.SplitN(parts[0], "?", 2)[0], ""
	}
	return strings.SplitN(parts[0], "?", 2)[0], parts[1]
}

func markdownAnchors(path string) (map[string]struct{}, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	result := make(map[string]struct{})
	duplicates := make(map[string]int)
	scanner := bufio.NewScanner(file)
	inCodeFence := false
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inCodeFence = !inCodeFence
			continue
		}
		if inCodeFence {
			continue
		}
		for _, match := range htmlIDPattern.FindAllStringSubmatch(line, -1) {
			result[strings.ToLower(match[1])] = struct{}{}
		}
		match := headingPattern.FindStringSubmatch(line)
		if len(match) == 0 {
			continue
		}
		base := githubSlug(match[2])
		if base == "" {
			continue
		}
		slug := base
		if count := duplicates[base]; count > 0 {
			slug = fmt.Sprintf("%s-%d", base, count)
		}
		duplicates[base]++
		result[slug] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func githubSlug(value string) string {
	value = htmlTagPattern.ReplaceAllString(value, "")
	value = strings.ReplaceAll(value, "`", "")
	value = strings.ToLower(strings.TrimSpace(value))
	var output strings.Builder
	previousDash := false
	for _, current := range value {
		switch {
		case unicode.IsLetter(current), unicode.IsNumber(current), current == '_':
			output.WriteRune(current)
			previousDash = false
		case unicode.IsSpace(current), current == '-':
			if !previousDash && output.Len() > 0 {
				output.WriteByte('-')
				previousDash = true
			}
		}
	}
	return strings.TrimSuffix(output.String(), "-")
}

func checkPublicTestReferences(root string) []string {
	tests := make(map[string]struct{})
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		if strings.HasSuffix(path, "_test.go") {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, name := range testFunctionPattern.FindAllString(string(raw), -1) {
				match := testFunctionPattern.FindStringSubmatch(name)
				tests[match[1]] = struct{}{}
			}
		}
		return nil
	})
	var failures []string
	for _, relative := range []string{"README.md", "README.en.md"} {
		path := filepath.Join(root, relative)
		raw, err := os.ReadFile(path)
		if err != nil {
			failures = append(failures, relative+": could not read public test references")
			continue
		}
		for _, testName := range testNamePattern.FindAllString(string(raw), -1) {
			if _, exists := tests[testName]; !exists {
				failures = append(failures, relative+": referenced test does not exist: "+testName)
			}
		}
	}
	return failures
}

func checkExternalLinks(ctx context.Context, links []documentLink) []string {
	type externalReference struct {
		url     string
		sources []documentLink
	}
	byURL := make(map[string][]documentLink)
	for _, link := range links {
		if strings.HasPrefix(link.target, "http://") ||
			strings.HasPrefix(link.target, "https://") {
			parsed, err := url.Parse(link.target)
			if err != nil {
				byURL[link.target] = append(byURL[link.target], link)
				continue
			}
			if isLoopbackHost(parsed.Hostname()) {
				continue
			}
			parsed.Fragment = ""
			byURL[parsed.String()] = append(byURL[parsed.String()], link)
		}
	}
	references := make([]externalReference, 0, len(byURL))
	for target, sources := range byURL {
		references = append(references, externalReference{url: target, sources: sources})
	}
	slices.SortFunc(references, func(left, right externalReference) int {
		return strings.Compare(left.url, right.url)
	})
	client := &http.Client{Timeout: 20 * time.Second}
	work := make(chan externalReference)
	failures := make(chan string, len(references))
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for reference := range work {
				status, err := probeURL(ctx, client, reference.url)
				if err == nil && reachableStatus(status) {
					continue
				}
				detail := "request failed"
				if err == nil {
					detail = fmt.Sprintf("HTTP %d", status)
				}
				failures <- formatLinkFailure(
					rootFromSource(reference.sources[0].source),
					reference.sources[0],
					detail,
				)
			}
		}()
	}
	for _, reference := range references {
		work <- reference
	}
	close(work)
	workers.Wait()
	close(failures)
	result := make([]string, 0, len(failures))
	for failure := range failures {
		result = append(result, failure)
	}
	return result
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func probeURL(ctx context.Context, client *http.Client, target string) (int, error) {
	status, err := requestURL(ctx, client, http.MethodHead, target)
	if err == nil && status != http.StatusMethodNotAllowed && status != http.StatusNotImplemented {
		return status, nil
	}
	return requestURL(ctx, client, http.MethodGet, target)
}

func requestURL(
	ctx context.Context,
	client *http.Client,
	method string,
	target string,
) (int, error) {
	request, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("User-Agent", "waybill-guardian-doc-check/1.0")
	if method == http.MethodGet {
		request.Header.Set("Range", "bytes=0-0")
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	_, _ = io.CopyN(io.Discard, response.Body, 1)
	_ = response.Body.Close()
	return response.StatusCode, nil
}

func reachableStatus(status int) bool {
	return status >= 200 && status < 400 ||
		status == http.StatusUnauthorized ||
		status == http.StatusForbidden ||
		status == http.StatusTooManyRequests
}

func formatLinkFailure(root string, link documentLink, detail string) string {
	source, err := filepath.Rel(root, link.source)
	if err != nil {
		source = link.source
	}
	return fmt.Sprintf("%s:%d: %s: %s", filepath.ToSlash(source), link.line, detail, link.target)
}

func rootFromSource(source string) string {
	current := filepath.Dir(source)
	for {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Dir(source)
		}
		current = parent
	}
}
