package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type S3API interface {
	PutObject(
		context.Context,
		*s3.PutObjectInput,
		...func(*s3.Options),
	) (*s3.PutObjectOutput, error)
	GetObject(
		context.Context,
		*s3.GetObjectInput,
		...func(*s3.Options),
	) (*s3.GetObjectOutput, error)
}

type S3Store struct {
	client   S3API
	bucket   string
	prefix   string
	maxBytes int64
}

func NewS3Store(
	client S3API,
	bucket string,
	prefix string,
	maxBytes int64,
) (*S3Store, error) {
	bucket = strings.TrimSpace(bucket)
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if client == nil || bucket == "" {
		return nil, fmt.Errorf("S3 artifact client and bucket are required")
	}
	if maxBytes <= 0 {
		return nil, fmt.Errorf("S3 artifact size limit must be positive")
	}
	if prefix == "." || prefix == ".." || strings.Contains(prefix, "/../") ||
		strings.HasPrefix(prefix, "../") || strings.HasSuffix(prefix, "/..") {
		return nil, fmt.Errorf("S3 artifact prefix must not contain parent traversal")
	}
	return &S3Store{
		client:   client,
		bucket:   bucket,
		prefix:   prefix,
		maxBytes: maxBytes,
	}, nil
}

func (store *S3Store) Put(
	ctx context.Context,
	tenantID domain.TenantID,
	value Artifact,
) (ArtifactRef, error) {
	if err := ctx.Err(); err != nil {
		return ArtifactRef{}, err
	}
	if value.digest == "" || len(value.envelope) == 0 {
		return ArtifactRef{}, fmt.Errorf("artifact was not constructed by artifact.New")
	}
	if int64(len(value.envelope)) > store.maxBytes {
		return ArtifactRef{}, fmt.Errorf("%w: %d bytes exceeds %d",
			ErrTooLarge, len(value.envelope), store.maxBytes)
	}
	key, err := store.objectKey(tenantID, value.digest)
	if err != nil {
		return ArtifactRef{}, err
	}
	for attempt := 0; attempt < 3; attempt++ {
		_, err = store.client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:        aws.String(store.bucket),
			Key:           aws.String(key),
			Body:          bytes.NewReader(value.envelope),
			ContentLength: aws.Int64(int64(len(value.envelope))),
			ContentType:   aws.String("application/json"),
			IfNoneMatch:   aws.String("*"),
			Metadata: map[string]string{
				"digest":         string(value.digest),
				"kind":           string(value.kind),
				"schema-version": value.schemaVersion,
			},
		})
		if err == nil {
			return value.Ref(), nil
		}
		if isS3PreconditionFailed(err) {
			if verifyErr := store.Verify(ctx, tenantID, value.digest); verifyErr != nil {
				return ArtifactRef{}, verifyErr
			}
			return value.Ref(), nil
		}
		if !isS3ConditionalConflict(err) {
			return ArtifactRef{}, fmt.Errorf("put S3 artifact: %w", err)
		}
	}
	return ArtifactRef{}, fmt.Errorf("put S3 artifact: conditional conflict persisted")
}

func (store *S3Store) Open(
	ctx context.Context,
	tenantID domain.TenantID,
	digest domain.ArtifactDigest,
) (io.ReadCloser, Metadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, Metadata{}, err
	}
	key, err := store.objectKey(tenantID, digest)
	if err != nil {
		return nil, Metadata{}, err
	}
	output, err := store.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(store.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isS3NotFound(err) {
			return nil, Metadata{}, ErrNotFound
		}
		return nil, Metadata{}, fmt.Errorf("get S3 artifact: %w", err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(output.Body, store.maxBytes+1))
	closeErr := output.Body.Close()
	if readErr != nil {
		return nil, Metadata{}, fmt.Errorf("read S3 artifact: %w", readErr)
	}
	if closeErr != nil {
		return nil, Metadata{}, fmt.Errorf("close S3 artifact: %w", closeErr)
	}
	if int64(len(raw)) > store.maxBytes {
		return nil, Metadata{}, fmt.Errorf("%w: stored S3 artifact exceeds %d bytes",
			ErrIntegrity, store.maxBytes)
	}
	value, err := parseEnvelope(raw, digest)
	if err != nil {
		return nil, Metadata{}, err
	}
	return io.NopCloser(bytes.NewReader(value.document)), value.Ref(), nil
}

func (store *S3Store) Verify(
	ctx context.Context,
	tenantID domain.TenantID,
	digest domain.ArtifactDigest,
) error {
	reader, _, err := store.Open(ctx, tenantID, digest)
	if err != nil {
		return err
	}
	return reader.Close()
}

func (store *S3Store) objectKey(
	tenantID domain.TenantID,
	digest domain.ArtifactDigest,
) (string, error) {
	tenant := string(tenantID)
	if tenant == "" || strings.TrimSpace(tenant) != tenant {
		return "", fmt.Errorf("tenant id is required")
	}
	decoded, err := hex.DecodeString(string(digest))
	if err != nil || len(decoded) != sha256.Size ||
		strings.ToLower(string(digest)) != string(digest) {
		return "", fmt.Errorf("artifact digest must be a lowercase SHA-256 value")
	}
	tenantSum := sha256.Sum256([]byte("delivery-artifact-tenant-v1\x00" + tenant))
	return path.Join(
		store.prefix,
		hex.EncodeToString(tenantSum[:]),
		string(digest)[:2],
		string(digest)+".json",
	), nil
}

func isS3PreconditionFailed(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) &&
		(apiErr.ErrorCode() == "PreconditionFailed" || apiErr.ErrorCode() == "412")
}

func isS3ConditionalConflict(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) &&
		apiErr.ErrorCode() == "ConditionalRequestConflict"
}

func isS3NotFound(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) &&
		(apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NotFound")
}
