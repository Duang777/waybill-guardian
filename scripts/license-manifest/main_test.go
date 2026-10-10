package main

import "testing"

func TestAssignLicensesUsesLongestModulePrefix(t *testing.T) {
	graph := map[string][]string{
		"example.com/root":        {"example.com/root"},
		"example.com/root/nested": {"example.com/root/nested"},
	}
	licenses := assignLicenses(graph, []licenseRecord{
		{
			Package: "example.com/root/pkg",
			URL:     "https://example.com/root/LICENSE",
			Type:    "Apache-2.0",
		},
		{
			Package: "example.com/root/nested/pkg",
			URL:     "https://example.com/nested/LICENSE",
			Type:    "MIT",
		},
	})
	if !hasModuleLicense(licenses, "example.com/root") {
		t.Fatal("root module license was not assigned")
	}
	if !hasModuleLicense(licenses, "example.com/root/nested") {
		t.Fatal("nested module license was not assigned")
	}
	if len(licenses) != 2 {
		t.Fatalf("license count = %d, want 2", len(licenses))
	}
}

func TestSortedMissingModulesReportsOnlyModulesWithoutLicense(t *testing.T) {
	graph := map[string][]string{
		"example.com/a": {"example.com/a"},
		"example.com/b": {"example.com/b"},
	}
	licenses := map[string]moduleLicense{
		"example.com/a\x00url\x00MIT": {
			Module: "example.com/a",
			URL:    "url",
			Type:   "MIT",
		},
	}
	missing := sortedMissingModules(graph, licenses)
	if len(missing) != 1 || missing[0] != "example.com/b" {
		t.Fatalf("missing modules = %v", missing)
	}
}
