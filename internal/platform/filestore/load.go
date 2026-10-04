package filestore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func Load(path string) (Loaded, error) {
	base := filepath.Base(path)
	if strings.TrimSpace(path) == "" || base == "." {
		return Loaded{}, fmt.Errorf("data file path is required")
	}
	format, err := formatForName(base)
	if err != nil {
		return Loaded{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Loaded{}, fmt.Errorf("open data file %q: unavailable", base)
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return Loaded{}, fmt.Errorf("read data file %q: failed", base)
	}
	if len(raw) > maxFileBytes {
		return Loaded{}, fmt.Errorf("data file %q exceeds %d bytes", base, maxFileBytes)
	}
	return loadBytes(format, raw)
}

func LoadEmbeddedJSON(name string, raw []byte) (Loaded, error) {
	if len(raw) > maxFileBytes {
		return Loaded{}, fmt.Errorf("embedded data %q exceeds %d bytes", filepath.Base(name), maxFileBytes)
	}
	return loadBytes("json", append([]byte(nil), raw...))
}

func formatForName(name string) (string, error) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".json":
		return "json", nil
	case ".csv":
		return "csv", nil
	default:
		return "", fmt.Errorf("data file %q must use .json or .csv", name)
	}
}

func loadBytes(format string, raw []byte) (Loaded, error) {
	var (
		draft datasetDraft
		err   error
	)
	switch format {
	case "json":
		draft, err = parseJSON(raw)
	case "csv":
		draft, err = parseCSV(raw)
	default:
		return Loaded{}, fmt.Errorf("unsupported data format %q", format)
	}
	if err != nil {
		return Loaded{}, err
	}
	snapshot, stats, err := validateAndBuild(draft)
	if err != nil {
		return Loaded{}, err
	}
	digest := sha256.Sum256(raw)
	source := SourceDescriptor{
		Format:        format,
		SchemaVersion: draft.SchemaVersion,
		DatasetID:     draft.DatasetID,
		Digest:        hex.EncodeToString(digest[:]),
	}
	return Loaded{
		Reads:  snapshot.readSet(),
		Source: source,
		Stats:  stats,
	}, nil
}
