package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/securefs"
)

type JSONProvider struct {
	document JSONDocument
}

func OpenJSON(path string, maxBytes int64) (*JSONProvider, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("delivery source JSON path is required")
	}
	if maxBytes <= 0 {
		return nil, fmt.Errorf("delivery source JSON size limit must be positive")
	}
	file, err := securefs.OpenExistingRegular(path, os.O_RDONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open delivery source JSON %q: %w", filepath.Base(path), err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, maxBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read delivery source JSON %q: %w", filepath.Base(path), readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close delivery source JSON %q: %w", filepath.Base(path), closeErr)
	}
	if int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("%w: JSON file exceeds %d bytes", ErrSourceTooLarge, maxBytes)
	}
	document, err := decodeJSONDocument(raw)
	if err != nil {
		return nil, err
	}
	if err := validateJSONDocument(document); err != nil {
		return nil, err
	}
	return &JSONProvider{document: document}, nil
}

func (provider *JSONProvider) OpenSnapshot(
	ctx context.Context,
	tenantID domain.TenantID,
	ref SnapshotRef,
) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tenantID != provider.document.Manifest.TenantID ||
		ref != provider.document.Manifest.Ref {
		return nil, ErrNotFound
	}
	return &jsonSnapshot{document: provider.document}, nil
}

type jsonSnapshot struct {
	document JSONDocument
}

func (snapshot *jsonSnapshot) ReadManifest(ctx context.Context) (Manifest, error) {
	return cloneOnRead(ctx, snapshot.document.Manifest)
}

func (snapshot *jsonSnapshot) ReadOrders(
	ctx context.Context,
	revision Revision,
) (Orders, Stamp, error) {
	return readJSONBlock(ctx, snapshot.document.Manifest, revision, snapshot.document.Orders)
}

func (snapshot *jsonSnapshot) ReadDepots(
	ctx context.Context,
	revision Revision,
) (Depots, Stamp, error) {
	return readJSONBlock(ctx, snapshot.document.Manifest, revision, snapshot.document.Depots)
}

func (snapshot *jsonSnapshot) ReadFleet(
	ctx context.Context,
	revision Revision,
) (Fleet, Stamp, error) {
	return readJSONBlock(ctx, snapshot.document.Manifest, revision, snapshot.document.Fleet)
}

func (snapshot *jsonSnapshot) ReadDrivers(
	ctx context.Context,
	revision Revision,
) (Drivers, Stamp, error) {
	return readJSONBlock(ctx, snapshot.document.Manifest, revision, snapshot.document.Drivers)
}

func (snapshot *jsonSnapshot) ReadTravel(
	ctx context.Context,
	revision Revision,
) (Travel, Stamp, error) {
	return readJSONBlock(ctx, snapshot.document.Manifest, revision, snapshot.document.Travel)
}

func (snapshot *jsonSnapshot) ReadChargers(
	ctx context.Context,
	revision Revision,
) (Chargers, Stamp, error) {
	return readJSONBlock(ctx, snapshot.document.Manifest, revision, snapshot.document.Chargers)
}

func (snapshot *jsonSnapshot) ReadPolicy(
	ctx context.Context,
	revision Revision,
) (Policy, Stamp, error) {
	return readJSONBlock(ctx, snapshot.document.Manifest, revision, snapshot.document.Policy)
}

func (snapshot *jsonSnapshot) Close(context.Context) error {
	return nil
}

func readJSONBlock[T any](
	ctx context.Context,
	manifest Manifest,
	revision Revision,
	block Block[T],
) (T, Stamp, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, Stamp{}, err
	}
	expected := revisionFor(manifest, block.Stamp.Kind)
	if revision != expected {
		return zero, Stamp{}, fmt.Errorf(
			"%w: requested %q revision does not match the JSON manifest",
			ErrSourceMismatch,
			block.Stamp.Kind,
		)
	}
	value, err := cloneOnRead(ctx, block.Data)
	if err != nil {
		return zero, Stamp{}, err
	}
	return value, block.Stamp, nil
}

func cloneOnRead[T any](ctx context.Context, value T) (T, error) {
	var result T
	if err := ctx.Err(); err != nil {
		return result, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return result, fmt.Errorf("copy delivery source value: %w", err)
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, fmt.Errorf("copy delivery source value: %w", err)
	}
	return result, nil
}

func decodeJSONDocument(raw []byte) (JSONDocument, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return JSONDocument{}, fmt.Errorf("delivery source JSON is empty")
	}
	if !utf8.Valid(raw) {
		return JSONDocument{}, fmt.Errorf("delivery source JSON is not valid UTF-8")
	}
	if err := rejectDuplicateJSONFields(raw); err != nil {
		return JSONDocument{}, err
	}
	var document JSONDocument
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return JSONDocument{}, fmt.Errorf("decode delivery source JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return JSONDocument{}, fmt.Errorf("delivery source JSON has a trailing value")
		}
		return JSONDocument{}, fmt.Errorf("decode trailing delivery source JSON: %w", err)
	}
	return document, nil
}

func validateJSONDocument(document JSONDocument) error {
	if document.SchemaVersion != JSONSchemaVersion {
		return fmt.Errorf("delivery source JSON schema_version must be %q", JSONSchemaVersion)
	}
	manifest, err := ValidateManifest(
		document.Manifest,
		document.Manifest.TenantID,
		document.Manifest.Ref,
		document.Manifest.IssuedAt,
	)
	if err != nil {
		return err
	}
	checks := []struct {
		kind  Kind
		stamp Stamp
		data  any
	}{
		{KindOrders, document.Orders.Stamp, document.Orders.Data},
		{KindDepots, document.Depots.Stamp, document.Depots.Data},
		{KindFleet, document.Fleet.Stamp, document.Fleet.Data},
		{KindDrivers, document.Drivers.Stamp, document.Drivers.Data},
		{KindTravel, document.Travel.Stamp, document.Travel.Data},
		{KindChargers, document.Chargers.Stamp, document.Chargers.Data},
		{KindPolicy, document.Policy.Stamp, document.Policy.Data},
	}
	for _, check := range checks {
		if err := validateBlock(
			manifest,
			revisionFor(manifest, check.kind),
			check.stamp,
			check.data,
		); err != nil {
			return fmt.Errorf("validate delivery source JSON: %w", err)
		}
	}
	return nil
}

func rejectDuplicateJSONFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode delivery source JSON: %w", err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return fmt.Errorf("delivery source JSON top-level value must be an object")
	}
	return rejectDuplicateJSONObject(decoder, "$")
}

func rejectDuplicateJSONObject(decoder *json.Decoder, location string) error {
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode delivery source JSON: %w", err)
		}
		field, ok := token.(string)
		if !ok {
			return fmt.Errorf("delivery source JSON object field name is invalid")
		}
		if _, exists := seen[field]; exists {
			return fmt.Errorf("delivery source JSON has duplicate field %q at %s", field, location)
		}
		seen[field] = struct{}{}
		if err := rejectDuplicateJSONValue(decoder, location+"."+field); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("decode delivery source JSON: %w", err)
	}
	return nil
}

func rejectDuplicateJSONValue(decoder *json.Decoder, location string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode delivery source JSON: %w", err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		return rejectDuplicateJSONObject(decoder, location)
	case '[':
		index := 0
		for decoder.More() {
			if err := rejectDuplicateJSONValue(
				decoder,
				fmt.Sprintf("%s[%d]", location, index),
			); err != nil {
				return err
			}
			index++
		}
		if _, err := decoder.Token(); err != nil {
			return fmt.Errorf("decode delivery source JSON: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("delivery source JSON has unexpected delimiter %q", delimiter)
	}
}
