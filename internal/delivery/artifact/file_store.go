package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/securefs"
)

type FileStore struct {
	root     string
	maxBytes int64
}

func NewFileStore(root string, maxBytes int64) (*FileStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("artifact root is required")
	}
	if maxBytes <= 0 {
		return nil, fmt.Errorf("artifact size limit must be positive")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact root: %w", err)
	}
	directory, err := openDurableDirectory(absolute, 0o700)
	if err != nil {
		return nil, fmt.Errorf("open artifact root: %w", err)
	}
	if err := directory.Close(); err != nil {
		return nil, fmt.Errorf("close artifact root: %w", err)
	}
	return &FileStore{root: absolute, maxBytes: maxBytes}, nil
}

func (store *FileStore) Put(
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
	path, err := store.artifactPath(tenantID, value.digest, true)
	if err != nil {
		return ArtifactRef{}, err
	}
	if _, statErr := os.Stat(path); statErr == nil {
		if err := store.Verify(ctx, tenantID, value.digest); err != nil {
			return ArtifactRef{}, err
		}
		return value.Ref(), nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return ArtifactRef{}, fmt.Errorf("inspect existing artifact: %w", statErr)
	}
	release, alreadyPublished, err := store.acquirePublishLock(ctx, path, tenantID, value.digest)
	if err != nil {
		return ArtifactRef{}, err
	}
	if alreadyPublished {
		return value.Ref(), nil
	}
	defer release()
	if _, statErr := os.Stat(path); statErr == nil {
		if err := store.Verify(ctx, tenantID, value.digest); err != nil {
			return ArtifactRef{}, err
		}
		return value.Ref(), nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return ArtifactRef{}, fmt.Errorf("inspect artifact after acquiring lock: %w", statErr)
	}

	temp, err := os.CreateTemp(filepath.Dir(path), ".put-*")
	if err != nil {
		return ArtifactRef{}, fmt.Errorf("create artifact staging file: %w", err)
	}
	tempPath := temp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()

	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return ArtifactRef{}, fmt.Errorf("set artifact staging permissions: %w", err)
	}
	if _, err := temp.Write(value.envelope); err != nil {
		_ = temp.Close()
		return ArtifactRef{}, fmt.Errorf("write artifact staging file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return ArtifactRef{}, fmt.Errorf("sync artifact staging file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return ArtifactRef{}, fmt.Errorf("close artifact staging file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return ArtifactRef{}, err
	}

	if err := os.Rename(tempPath, path); err != nil {
		return ArtifactRef{}, fmt.Errorf("publish artifact: %w", err)
	}
	removeTemp = false
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return ArtifactRef{}, err
	}
	return value.Ref(), nil
}

func (store *FileStore) acquirePublishLock(
	ctx context.Context,
	path string,
	tenantID domain.TenantID,
	digest domain.ArtifactDigest,
) (func(), bool, error) {
	lockPath := path + ".lock"
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if closeErr := lock.Close(); closeErr != nil {
				_ = os.Remove(lockPath)
				return nil, false, fmt.Errorf("close artifact publish lock: %w", closeErr)
			}
			return func() {
				_ = os.Remove(lockPath)
			}, false, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, false, fmt.Errorf("acquire artifact publish lock: %w", err)
		}
		if _, statErr := os.Stat(path); statErr == nil {
			if verifyErr := store.Verify(ctx, tenantID, digest); verifyErr != nil {
				return nil, false, verifyErr
			}
			if syncErr := syncDirectory(filepath.Dir(path)); syncErr != nil {
				return nil, false, syncErr
			}
			return func() {}, true, nil
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, false, fmt.Errorf("inspect artifact while waiting for publish: %w", statErr)
		}
		info, statErr := os.Lstat(lockPath)
		if statErr == nil && time.Since(info.ModTime()) > time.Minute {
			if removeErr := os.Remove(lockPath); removeErr != nil &&
				!errors.Is(removeErr, os.ErrNotExist) {
				return nil, false, fmt.Errorf("reclaim stale artifact publish lock: %w", removeErr)
			}
			continue
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, false, ctx.Err()
		case <-timer.C:
		}
	}
}

func (store *FileStore) Open(
	ctx context.Context,
	tenantID domain.TenantID,
	digest domain.ArtifactDigest,
) (io.ReadCloser, Metadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, Metadata{}, err
	}
	path, err := store.artifactPath(tenantID, digest, false)
	if err != nil {
		return nil, Metadata{}, err
	}
	file, err := securefs.OpenExistingRegular(path, os.O_RDONLY, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return nil, Metadata{}, ErrNotFound
	}
	if err != nil {
		return nil, Metadata{}, fmt.Errorf("open artifact: %w", err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, store.maxBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, Metadata{}, fmt.Errorf("read artifact: %w", readErr)
	}
	if closeErr != nil {
		return nil, Metadata{}, fmt.Errorf("close artifact: %w", closeErr)
	}
	if int64(len(raw)) > store.maxBytes {
		return nil, Metadata{}, fmt.Errorf("%w: stored artifact exceeds %d bytes",
			ErrIntegrity, store.maxBytes)
	}
	if err := ctx.Err(); err != nil {
		return nil, Metadata{}, err
	}
	value, err := parseEnvelope(raw, digest)
	if err != nil {
		return nil, Metadata{}, err
	}
	ref := value.Ref()
	return io.NopCloser(bytes.NewReader(value.document)), ref, nil
}

func (store *FileStore) Verify(
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

func (store *FileStore) artifactPath(
	tenantID domain.TenantID,
	digest domain.ArtifactDigest,
	createDirectories bool,
) (string, error) {
	tenant := string(tenantID)
	if tenant == "" || strings.TrimSpace(tenant) != tenant {
		return "", fmt.Errorf("tenant id is required")
	}
	digestBytes, err := hex.DecodeString(string(digest))
	if err != nil || len(digestBytes) != sha256.Size ||
		strings.ToLower(string(digest)) != string(digest) {
		return "", fmt.Errorf("artifact digest must be a lowercase SHA-256 value")
	}
	tenantSum := sha256.Sum256([]byte("delivery-artifact-tenant-v1\x00" + tenant))
	tenantDirectory := filepath.Join(store.root, hex.EncodeToString(tenantSum[:]))
	shardDirectory := filepath.Join(tenantDirectory, string(digest)[:2])
	if createDirectories {
		directory, err := securefs.OpenDirectory(tenantDirectory, 0o700)
		if err != nil {
			return "", fmt.Errorf("open tenant artifact directory: %w", err)
		}
		if err := directory.Close(); err != nil {
			return "", fmt.Errorf("close tenant artifact directory: %w", err)
		}
		if err := syncDirectory(store.root); err != nil {
			return "", err
		}
		directory, err = securefs.OpenDirectory(shardDirectory, 0o700)
		if err != nil {
			return "", fmt.Errorf("open artifact shard directory: %w", err)
		}
		if err := directory.Close(); err != nil {
			return "", fmt.Errorf("close artifact shard directory: %w", err)
		}
		if err := syncDirectory(tenantDirectory); err != nil {
			return "", err
		}
	}
	return filepath.Join(shardDirectory, string(digest)+".artifact"), nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open artifact directory for sync: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return fmt.Errorf("sync artifact directory: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close artifact directory after sync: %w", closeErr)
	}
	return nil
}

func openDurableDirectory(path string, perm os.FileMode) (*os.File, error) {
	cursor := path
	missing := make([]string, 0)
	for {
		info, err := os.Lstat(cursor)
		if err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("path is not a physical directory")
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			return nil, fmt.Errorf("no existing parent for artifact directory")
		}
		missing = append(missing, filepath.Base(cursor))
		cursor = parent
	}
	for index := len(missing) - 1; index >= 0; index-- {
		next := filepath.Join(cursor, missing[index])
		created := false
		if err := os.Mkdir(next, perm); err == nil {
			created = true
		} else if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		directory, err := securefs.OpenDirectory(next, perm)
		if err != nil {
			return nil, err
		}
		if err := directory.Close(); err != nil {
			return nil, err
		}
		if created {
			if err := syncDirectory(cursor); err != nil {
				return nil, err
			}
		}
		cursor = next
	}
	return securefs.OpenDirectory(path, perm)
}
