package fs

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const utf8BOM = "\ufeff"

func normalizeText(data []byte) (text string, hadBOM, hadCRLF bool) {
	if bytes.HasPrefix(data, []byte(utf8BOM)) {
		data = data[3:]
		hadBOM = true
	}
	if bytes.Contains(data, []byte("\r\n")) {
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
		hadCRLF = true
	}
	return string(data), hadBOM, hadCRLF
}

func restoreFormat(text string, hadBOM, hadCRLF bool) []byte {
	if hadCRLF {
		text = strings.ReplaceAll(text, "\r\n", "\n")
		text = strings.ReplaceAll(text, "\n", "\r\n")
	}
	if hadBOM && !strings.HasPrefix(text, utf8BOM) {
		return append([]byte(utf8BOM), text...)
	}
	return []byte(text)
}

const (
	temporaryWritePrefix       = ".write-"
	temporaryWriteEntropyBytes = 16
)

// A sibling temporary file keeps POSIX rename atomic. Nil preservedMode uses
// defaultFileMode subject to umask; existing files retain exact permissions.
func atomicWriteRootFile(root *os.Root, path string, data []byte, preservedMode *os.FileMode) (err error) {
	dir := filepath.Dir(path)
	mode := defaultFileMode
	if preservedMode != nil {
		mode = *preservedMode
	}
	name, err := temporaryWriteName()
	if err != nil {
		return err
	}
	tmpPath := filepath.Join(dir, name)
	tmp, err := root.OpenFile(
		tmpPath,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		mode,
	)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			if cleanupErr := root.Remove(tmpPath); !errors.Is(cleanupErr, os.ErrNotExist) {
				err = errors.Join(err, cleanupErr)
			}
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		err = errors.Join(err, tmp.Close())
		return err
	}
	if err = tmp.Sync(); err != nil {
		err = errors.Join(err, tmp.Close())
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if preservedMode != nil {
		if err = root.Chmod(tmpPath, *preservedMode); err != nil {
			return err
		}
	}
	return root.Rename(tmpPath, path)
}

func temporaryWriteName() (string, error) {
	var entropy [temporaryWriteEntropyBytes]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", fmt.Errorf("fs: create temporary write name: %w", err)
	}
	return temporaryWritePrefix + hex.EncodeToString(entropy[:]), nil
}
