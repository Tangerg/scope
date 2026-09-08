package fs

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// utf8BOM is the UTF-8 byte-order-mark. Some editors / Windows tools
// write it; the LLM shouldn't see it and shouldn't need to type it.
const utf8BOM = "\ufeff"

// normalizeText strips UTF-8 BOM and converts CRLF to LF. Returns the
// normalized content as a string plus flags so a downstream Write can
// restore the original format.
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

// restoreFormat preserves the original file's CRLF and BOM without duplicating
// formatting already present in replacement text.
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

// atomicWriteRootFile writes data to path through a sibling temp file +
// rename. On POSIX the rename is atomic as long as both paths are on
// the same filesystem — so partial writes never leave a half-written
// file visible to readers.
func atomicWriteRootFile(root *os.Root, path string, data []byte, mode os.FileMode) (err error) {
	dir := filepath.Dir(path)
	if err = root.MkdirAll(dir, defaultDirectoryMode); err != nil {
		return err
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
			_ = root.Remove(tmpPath)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = root.Chmod(tmpPath, mode); err != nil {
		return err
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
