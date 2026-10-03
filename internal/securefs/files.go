package securefs

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func OpenDirectory(path string, perm os.FileMode) (*os.File, error) {
	if err := os.MkdirAll(path, perm); err != nil {
		return nil, fmt.Errorf("create directory: %w", err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect directory: %w", err)
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("path is not a physical directory")
	}
	dir, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open directory: %w", err)
	}
	after, err := dir.Stat()
	if err != nil {
		_ = dir.Close()
		return nil, fmt.Errorf("inspect opened directory: %w", err)
	}
	if !after.IsDir() || !os.SameFile(before, after) {
		_ = dir.Close()
		return nil, fmt.Errorf("directory changed while opening")
	}
	if err := dir.Chmod(perm); err != nil {
		_ = dir.Close()
		return nil, fmt.Errorf("set directory permissions: %w", err)
	}
	return dir, nil
}

func OpenExistingRegular(path string, flag int, perm os.FileMode) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if err := validateRegular(before); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !os.SameFile(before, after) {
		_ = file.Close()
		return nil, fmt.Errorf("file changed while opening")
	}
	if err := validateRegular(after); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Chmod(perm); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func OpenOrCreateRegular(path string, flag int, perm os.FileMode) (*os.File, bool, error) {
	for {
		file, err := OpenExistingRegular(path, flag, perm)
		if err == nil {
			return file, false, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, false, err
		}

		file, err = os.OpenFile(path, flag|os.O_CREATE|os.O_EXCL, perm)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		info, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			return nil, false, statErr
		}
		if err := validateRegular(info); err != nil {
			_ = file.Close()
			return nil, false, err
		}
		if err := file.Chmod(perm); err != nil {
			_ = file.Close()
			return nil, false, err
		}
		return file, true, nil
	}
}

func validateRegular(info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("path is not a regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("file link count is unavailable")
	}
	if stat.Nlink != 1 {
		return fmt.Errorf("file has %d hard links, want 1", stat.Nlink)
	}
	return nil
}
