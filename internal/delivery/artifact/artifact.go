package artifact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

var (
	ErrNotFound  = errors.New("artifact not found")
	ErrIntegrity = errors.New("artifact integrity check failed")
	ErrTooLarge  = errors.New("artifact exceeds size limit")
)

type Kind string

const (
	KindProblem          Kind = "problem"
	KindPlan             Kind = "plan"
	KindValidationReport Kind = "validation_report"
	KindMatrix           Kind = "matrix"
	KindLoad             Kind = "load"
	KindEvidence         Kind = "evidence"
	KindEffect           Kind = "effect"
)

type Artifact struct {
	kind          Kind
	schemaVersion string
	document      []byte
	envelope      []byte
	digest        domain.ArtifactDigest
}

type ArtifactRef struct {
	Digest        domain.ArtifactDigest `json:"digest"`
	Kind          Kind                  `json:"kind"`
	SchemaVersion string                `json:"schema_version"`
	SizeBytes     int64                 `json:"size_bytes"`
}

type Metadata = ArtifactRef

type Store interface {
	Put(context.Context, domain.TenantID, Artifact) (ArtifactRef, error)
	Open(
		context.Context,
		domain.TenantID,
		domain.ArtifactDigest,
	) (io.ReadCloser, Metadata, error)
	Verify(context.Context, domain.TenantID, domain.ArtifactDigest) error
}

type envelope struct {
	SchemaVersion         string          `json:"schema_version"`
	Kind                  Kind            `json:"kind"`
	DocumentSchemaVersion string          `json:"document_schema_version"`
	Document              json.RawMessage `json:"document"`
}

func New(kind Kind, schemaVersion string, value any) (Artifact, error) {
	document, err := domain.CanonicalJSON(value)
	if err != nil {
		return Artifact{}, fmt.Errorf("canonicalize artifact document: %w", err)
	}
	return NewCanonical(kind, schemaVersion, document)
}

func NewCanonical(kind Kind, schemaVersion string, document []byte) (Artifact, error) {
	if !validKind(kind) {
		return Artifact{}, fmt.Errorf("unsupported artifact kind %q", kind)
	}
	if schemaVersion == "" || strings.TrimSpace(schemaVersion) != schemaVersion {
		return Artifact{}, fmt.Errorf("artifact schema version is required")
	}
	canonicalDocument, err := domain.CanonicalizeJSON(document)
	if err != nil {
		return Artifact{}, fmt.Errorf("canonicalize artifact document: %w", err)
	}
	canonicalEnvelope, err := domain.CanonicalJSON(envelope{
		SchemaVersion:         domain.ArtifactSchemaVersion,
		Kind:                  kind,
		DocumentSchemaVersion: schemaVersion,
		Document:              canonicalDocument,
	})
	if err != nil {
		return Artifact{}, fmt.Errorf("canonicalize artifact envelope: %w", err)
	}
	digest, err := domain.DigestCanonicalJSON(canonicalEnvelope)
	if err != nil {
		return Artifact{}, fmt.Errorf("digest artifact envelope: %w", err)
	}
	return Artifact{
		kind:          kind,
		schemaVersion: schemaVersion,
		document:      append([]byte(nil), canonicalDocument...),
		envelope:      canonicalEnvelope,
		digest:        digest,
	}, nil
}

func (value Artifact) Digest() domain.ArtifactDigest {
	return value.digest
}

func (value Artifact) Ref() ArtifactRef {
	return ArtifactRef{
		Digest:        value.digest,
		Kind:          value.kind,
		SchemaVersion: value.schemaVersion,
		SizeBytes:     int64(len(value.document)),
	}
}

func (value Artifact) Document() []byte {
	return append([]byte(nil), value.document...)
}

func parseEnvelope(raw []byte, expected domain.ArtifactDigest) (Artifact, error) {
	actual, err := domain.DigestCanonicalJSON(raw)
	if err != nil {
		return Artifact{}, fmt.Errorf("%w: canonical envelope: %v", ErrIntegrity, err)
	}
	if actual != expected {
		return Artifact{}, fmt.Errorf("%w: digest is %s, want %s", ErrIntegrity, actual, expected)
	}
	canonical, err := domain.CanonicalizeJSON(raw)
	if err != nil || !bytes.Equal(canonical, raw) {
		return Artifact{}, fmt.Errorf("%w: stored envelope is not canonical", ErrIntegrity)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var stored envelope
	if err := decoder.Decode(&stored); err != nil {
		return Artifact{}, fmt.Errorf("%w: decode envelope: %v", ErrIntegrity, err)
	}
	if stored.SchemaVersion != domain.ArtifactSchemaVersion ||
		stored.DocumentSchemaVersion == "" ||
		!validKind(stored.Kind) {
		return Artifact{}, fmt.Errorf("%w: unsupported envelope metadata", ErrIntegrity)
	}
	document, err := domain.CanonicalizeJSON(stored.Document)
	if err != nil || !bytes.Equal(document, stored.Document) {
		return Artifact{}, fmt.Errorf("%w: document is not canonical", ErrIntegrity)
	}

	return Artifact{
		kind:          stored.Kind,
		schemaVersion: stored.DocumentSchemaVersion,
		document:      document,
		envelope:      append([]byte(nil), raw...),
		digest:        actual,
	}, nil
}

func validKind(value Kind) bool {
	switch value {
	case KindProblem, KindPlan, KindValidationReport, KindMatrix, KindLoad, KindEvidence, KindEffect:
		return true
	default:
		return false
	}
}
