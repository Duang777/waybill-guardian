package source

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

const (
	ManifestSchemaVersion = "delivery.source-manifest.v1"
	JSONSchemaVersion     = "delivery.source-json.v1"
)

var (
	ErrNotFound          = errors.New("delivery source snapshot not found")
	ErrManifestMismatch  = errors.New("delivery source manifest mismatch")
	ErrSourceMismatch    = errors.New("delivery source revision mismatch")
	ErrSourceTooLarge    = errors.New("delivery source exceeds configured limits")
	ErrUntrustedEndpoint = errors.New("delivery source endpoint is not trusted")
)

type SnapshotRef string

type Kind string

const (
	KindOrders   Kind = "orders"
	KindDepots   Kind = "depots"
	KindFleet    Kind = "fleet"
	KindDrivers  Kind = "drivers"
	KindTravel   Kind = "travel"
	KindChargers Kind = "chargers"
	KindPolicy   Kind = "policy"
)

var allKinds = []Kind{
	KindOrders,
	KindDepots,
	KindFleet,
	KindDrivers,
	KindTravel,
	KindChargers,
	KindPolicy,
}

var kindRank = map[Kind]int{
	KindOrders:   0,
	KindDepots:   1,
	KindFleet:    2,
	KindDrivers:  3,
	KindTravel:   4,
	KindChargers: 5,
	KindPolicy:   6,
}

type Revision struct {
	Kind          Kind                  `json:"kind"`
	System        string                `json:"system"`
	Revision      string                `json:"revision"`
	EffectiveAt   time.Time             `json:"effective_at"`
	ETag          string                `json:"etag"`
	EventOffset   string                `json:"event_offset"`
	ContentDigest domain.ArtifactDigest `json:"content_digest"`
}

type Manifest struct {
	SchemaVersion string                `json:"schema_version"`
	TenantID      domain.TenantID       `json:"tenant_id"`
	Ref           SnapshotRef           `json:"ref"`
	IssuedAt      time.Time             `json:"issued_at"`
	ExpiresAt     time.Time             `json:"expires_at"`
	Revisions     []Revision            `json:"revisions"`
	Digest        domain.ArtifactDigest `json:"digest"`
}

type Stamp struct {
	TenantID      domain.TenantID       `json:"tenant_id"`
	ManifestRef   SnapshotRef           `json:"manifest_ref"`
	Kind          Kind                  `json:"kind"`
	System        string                `json:"system"`
	Revision      string                `json:"revision"`
	EffectiveAt   time.Time             `json:"effective_at"`
	FetchedAt     time.Time             `json:"fetched_at"`
	ETag          string                `json:"etag"`
	EventOffset   string                `json:"event_offset"`
	ContentDigest domain.ArtifactDigest `json:"content_digest"`
}

type Orders struct {
	Locations []domain.Location         `json:"locations"`
	Requests  []domain.TransportRequest `json:"requests"`
	Units     []domain.FulfillmentUnit  `json:"units"`
	Cargo     []domain.CargoItem        `json:"cargo"`
}

type Depots struct {
	Locations []domain.Location `json:"locations"`
	Depots    []domain.Depot    `json:"depots"`
}

type Fleet struct {
	Vehicles []domain.Vehicle `json:"vehicles"`
}

type Drivers struct {
	Drivers []domain.Driver `json:"drivers"`
}

type Travel struct {
	Locations []domain.Location   `json:"locations"`
	Travel    domain.TravelMatrix `json:"travel"`
	Energy    domain.EnergyMatrix `json:"energy"`
}

type Chargers struct {
	Locations []domain.Location        `json:"locations"`
	Chargers  []domain.ChargingStation `json:"chargers"`
}

type Policy struct {
	Policy domain.PlanningPolicy `json:"policy"`
}

type Block[T any] struct {
	Stamp Stamp `json:"stamp"`
	Data  T     `json:"data"`
}

type JSONDocument struct {
	SchemaVersion string          `json:"schema_version"`
	Manifest      Manifest        `json:"manifest"`
	Orders        Block[Orders]   `json:"orders"`
	Depots        Block[Depots]   `json:"depots"`
	Fleet         Block[Fleet]    `json:"fleet"`
	Drivers       Block[Drivers]  `json:"drivers"`
	Travel        Block[Travel]   `json:"travel"`
	Chargers      Block[Chargers] `json:"chargers"`
	Policy        Block[Policy]   `json:"policy"`
}

type ManifestReader interface {
	ReadManifest(context.Context) (Manifest, error)
}

type OrderReader interface {
	ReadOrders(context.Context, Revision) (Orders, Stamp, error)
}

type DepotReader interface {
	ReadDepots(context.Context, Revision) (Depots, Stamp, error)
}

type FleetReader interface {
	ReadFleet(context.Context, Revision) (Fleet, Stamp, error)
}

type DriverReader interface {
	ReadDrivers(context.Context, Revision) (Drivers, Stamp, error)
}

type TravelMatrixReader interface {
	ReadTravel(context.Context, Revision) (Travel, Stamp, error)
}

type ChargerReader interface {
	ReadChargers(context.Context, Revision) (Chargers, Stamp, error)
}

type PolicyReader interface {
	ReadPolicy(context.Context, Revision) (Policy, Stamp, error)
}

type Snapshot interface {
	ManifestReader
	OrderReader
	DepotReader
	FleetReader
	DriverReader
	TravelMatrixReader
	ChargerReader
	PolicyReader
	Close(context.Context) error
}

type Provider interface {
	OpenSnapshot(
		context.Context,
		domain.TenantID,
		SnapshotRef,
	) (Snapshot, error)
}

func SealManifest(value Manifest) (Manifest, error) {
	value.Digest = ""
	normalized, err := normalizeManifest(value)
	if err != nil {
		return Manifest{}, err
	}
	normalized.Digest, err = domain.Digest(normalized)
	if err != nil {
		return Manifest{}, fmt.Errorf("digest source manifest: %w", err)
	}
	return normalized, nil
}

func ValidateManifest(
	value Manifest,
	tenantID domain.TenantID,
	ref SnapshotRef,
	now time.Time,
) (Manifest, error) {
	expectedDigest := value.Digest
	if !validDigest(expectedDigest) {
		return Manifest{}, fmt.Errorf("%w: manifest digest must be a lowercase SHA-256 value",
			ErrManifestMismatch)
	}
	value.Digest = ""
	normalized, err := normalizeManifest(value)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrManifestMismatch, err)
	}
	actualDigest, err := domain.Digest(normalized)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: digest manifest: %v", ErrManifestMismatch, err)
	}
	if expectedDigest != actualDigest {
		return Manifest{}, fmt.Errorf("%w: digest is %s, want %s",
			ErrManifestMismatch, expectedDigest, actualDigest)
	}
	normalized.Digest = actualDigest
	if normalized.TenantID != tenantID || normalized.Ref != ref {
		return Manifest{}, fmt.Errorf(
			"%w: tenant/ref is %q/%q, want %q/%q",
			ErrManifestMismatch,
			normalized.TenantID,
			normalized.Ref,
			tenantID,
			ref,
		)
	}
	if now.IsZero() {
		return Manifest{}, fmt.Errorf("%w: validation time is required", ErrManifestMismatch)
	}
	if !now.UTC().Before(normalized.ExpiresAt) {
		return Manifest{}, fmt.Errorf("%w: manifest expired at %s",
			ErrManifestMismatch, normalized.ExpiresAt.Format(time.RFC3339Nano))
	}
	return normalized, nil
}

func normalizeManifest(value Manifest) (Manifest, error) {
	if value.SchemaVersion != ManifestSchemaVersion {
		return Manifest{}, fmt.Errorf("schema_version must be %q", ManifestSchemaVersion)
	}
	if strings.TrimSpace(string(value.TenantID)) == "" ||
		strings.TrimSpace(string(value.TenantID)) != string(value.TenantID) {
		return Manifest{}, errors.New("tenant_id is required without surrounding whitespace")
	}
	if strings.TrimSpace(string(value.Ref)) == "" ||
		strings.TrimSpace(string(value.Ref)) != string(value.Ref) {
		return Manifest{}, errors.New("ref is required without surrounding whitespace")
	}
	if !isUTC(value.IssuedAt) || !isUTC(value.ExpiresAt) ||
		value.IssuedAt.IsZero() || !value.IssuedAt.Before(value.ExpiresAt) {
		return Manifest{}, errors.New("issued_at and expires_at must be an increasing UTC interval")
	}
	value.IssuedAt = value.IssuedAt.UTC()
	value.ExpiresAt = value.ExpiresAt.UTC()
	value.Revisions = slices.Clone(value.Revisions)
	slices.SortFunc(value.Revisions, func(left, right Revision) int {
		return kindRank[left.Kind] - kindRank[right.Kind]
	})
	if len(value.Revisions) != len(allKinds) {
		return Manifest{}, fmt.Errorf("manifest must contain exactly %d source revisions", len(allKinds))
	}
	for index, kind := range allKinds {
		revision := &value.Revisions[index]
		if revision.Kind != kind {
			return Manifest{}, fmt.Errorf("manifest source %d is %q, want %q",
				index, revision.Kind, kind)
		}
		if strings.TrimSpace(revision.System) == "" ||
			strings.TrimSpace(revision.System) != revision.System ||
			strings.TrimSpace(revision.Revision) == "" ||
			strings.TrimSpace(revision.Revision) != revision.Revision {
			return Manifest{}, fmt.Errorf("source %q system and revision are required", kind)
		}
		if !isUTC(revision.EffectiveAt) || revision.EffectiveAt.IsZero() ||
			revision.EffectiveAt.After(value.IssuedAt) {
			return Manifest{}, fmt.Errorf(
				"source %q effective_at must be UTC and not after issued_at",
				kind,
			)
		}
		revision.EffectiveAt = revision.EffectiveAt.UTC()
		if revision.ETag == "" && revision.EventOffset == "" {
			return Manifest{}, fmt.Errorf("source %q requires an etag or event offset", kind)
		}
		if !validDigest(revision.ContentDigest) {
			return Manifest{}, fmt.Errorf(
				"source %q content_digest must be a lowercase SHA-256 value",
				kind,
			)
		}
	}
	return value, nil
}

func revisionFor(manifest Manifest, kind Kind) Revision {
	for _, revision := range manifest.Revisions {
		if revision.Kind == kind {
			return revision
		}
	}
	panic("validated manifest is missing " + string(kind))
}

func validateBlock[T any](
	manifest Manifest,
	revision Revision,
	stamp Stamp,
	data T,
) error {
	if stamp.TenantID != manifest.TenantID ||
		stamp.ManifestRef != manifest.Ref ||
		stamp.Kind != revision.Kind ||
		stamp.System != revision.System ||
		stamp.Revision != revision.Revision ||
		!stamp.EffectiveAt.Equal(revision.EffectiveAt) ||
		stamp.ETag != revision.ETag ||
		stamp.EventOffset != revision.EventOffset ||
		stamp.ContentDigest != revision.ContentDigest {
		return fmt.Errorf("%w: stamp for %q does not match manifest revision",
			ErrSourceMismatch, revision.Kind)
	}
	if !isUTC(stamp.EffectiveAt) || !isUTC(stamp.FetchedAt) ||
		stamp.FetchedAt.IsZero() || stamp.FetchedAt.Before(stamp.EffectiveAt) {
		return fmt.Errorf(
			"%w: source %q fetched_at must be UTC and not before effective_at",
			ErrSourceMismatch,
			revision.Kind,
		)
	}
	actual, err := domain.Digest(data)
	if err != nil {
		return fmt.Errorf("%w: digest source %q: %v", ErrSourceMismatch, revision.Kind, err)
	}
	if actual != revision.ContentDigest {
		return fmt.Errorf("%w: source %q content digest is %s, want %s",
			ErrSourceMismatch, revision.Kind, actual, revision.ContentDigest)
	}
	return nil
}

func validDigest(value domain.ArtifactDigest) bool {
	decoded, err := hex.DecodeString(string(value))
	return err == nil && len(decoded) == 32 &&
		strings.ToLower(string(value)) == string(value)
}

func isUTC(value time.Time) bool {
	_, offset := value.Zone()
	return offset == 0
}
