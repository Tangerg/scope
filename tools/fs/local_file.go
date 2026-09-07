package fs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Tangerg/scope/tools/textread"
)

const (
	defaultReadInputBytes     int64 = 8 << 20
	defaultReadLineBytes            = 1 << 20
	defaultReadOutputBytes          = 1 << 20
	defaultMutationInputBytes int64 = 8 << 20
	formatDetectionBytes      int64 = 64 << 10
)

// Read does not lock — concurrent reads are fine and a slightly stale
// read while another goroutine writes is acceptable (atomic-rename in
// Write means the caller sees either the old file in full or the new file in
// full, never a torn write).
func (l *LocalExecutor) Read(ctx context.Context, in ReadInput) (_ ReadOutput, err error) {
	if in.Limit < 0 || in.MaxInputBytes < 0 || in.MaxLineBytes < 0 || in.MaxOutputBytes < 0 {
		return ReadOutput{}, fmt.Errorf("%w: read limits must not be negative", ErrInvalidInput)
	}
	path, err := l.authorize(in.Path, false)
	if err != nil {
		return ReadOutput{}, err
	}
	root, err := l.openRoot()
	if err != nil {
		return ReadOutput{}, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()
	file, info, err := openRegularRootFile(ctx, root, path)
	if err != nil {
		return ReadOutput{}, err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()

	limits := in.resolvedLimits()
	if info.Size() > limits.inputBytes {
		return ReadOutput{}, fmt.Errorf("%w: %s uses %d bytes; limit is %d", ErrFileTooLarge, in.Path, info.Size(), limits.inputBytes)
	}
	result, err := textread.Scan(ctx, file, textread.Options{
		InputBytes: limits.inputBytes, LineBytes: limits.lineBytes,
		OutputBytes: limits.outputBytes, StartLine: in.Offset, MaxLines: in.Limit,
		PartialLine: in.PartialLine,
	})
	if err != nil {
		switch {
		case errors.Is(err, textread.ErrInputTooLarge):
			return ReadOutput{}, fmt.Errorf("%w: %s grew beyond %d bytes", ErrFileTooLarge, in.Path, limits.inputBytes)
		case errors.Is(err, textread.ErrLineTooLarge):
			return ReadOutput{}, &lineLimitError{
				path: in.Path, line: textread.LineNumber(err), limit: limits.lineBytes,
			}
		case errors.Is(err, textread.ErrInvalidText):
			return ReadOutput{}, ErrBinaryFile
		default:
			return ReadOutput{}, fmt.Errorf("fs.LocalExecutor.Read: scan %s: %w", in.Path, err)
		}
	}

	return ReadOutput{
		Content: result.Content, StartLine: result.StartLine, EndLine: result.EndLine,
		TotalLines: result.TotalLines, Truncated: result.Truncated,
	}, nil
}

type readLimits struct {
	inputBytes  int64
	lineBytes   int
	outputBytes int
}

func (r ReadInput) resolvedLimits() readLimits {
	return readLimits{
		inputBytes:  positiveOr(r.MaxInputBytes, defaultReadInputBytes),
		lineBytes:   positiveOr(r.MaxLineBytes, defaultReadLineBytes),
		outputBytes: positiveOr(r.MaxOutputBytes, defaultReadOutputBytes),
	}
}

func positiveOr[T ~int | ~int64](value, fallback T) T {
	if value > 0 {
		return value
	}
	return fallback
}

func (l *LocalExecutor) Write(ctx context.Context, in WriteRequest) (_ WriteResponse, err error) {
	if strings.ContainsRune(in.Content, 0) {
		return WriteResponse{}, fmt.Errorf("fs.LocalExecutor.Write: %w", ErrBinaryFile)
	}
	path, err := l.authorize(in.Path, false)
	if err != nil {
		return WriteResponse{}, err
	}
	root, err := l.openRoot()
	if err != nil {
		return WriteResponse{}, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()

	unlock := l.lockPath(path)
	defer unlock()
	if cause := context.Cause(ctx); cause != nil {
		return WriteResponse{}, cause
	}

	// Detect existing format + permissions so an overwrite preserves
	// CRLF / BOM / mode instead of silently flipping them.
	mode := defaultFileMode
	hadBOM, hadCRLF := false, false
	if info, statErr := root.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
		hadBOM, hadCRLF, err = detectRootFormat(ctx, root, path)
		if err != nil {
			return WriteResponse{}, err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return WriteResponse{}, statErr
	}

	out := restoreFormat(in.Content, hadBOM, hadCRLF)
	if err := atomicWriteRootFile(root, path, out, mode); err != nil {
		return WriteResponse{}, err
	}
	return WriteResponse{BytesWritten: len(out)}, nil
}

func (l *LocalExecutor) Edit(ctx context.Context, in EditRequest) (_ EditResponse, err error) {
	path, err := l.authorize(in.Path, false)
	if err != nil {
		return EditResponse{}, err
	}
	root, err := l.openRoot()
	if err != nil {
		return EditResponse{}, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()

	unlock := l.lockPath(path)
	defer unlock()

	data, err := readBoundedRootFile(ctx, root, path, defaultMutationInputBytes)
	if err != nil {
		return EditResponse{}, err
	}
	if looksBinary(data) {
		return EditResponse{}, ErrBinaryFile
	}

	content, hadBOM, hadCRLF := normalizeText(data)
	updated, replacements, err := (editOperation{
		OldString:  in.OldString,
		NewString:  in.NewString,
		ReplaceAll: in.ReplaceAll,
	}).apply(content, in.Path)
	if err != nil {
		return EditResponse{}, err
	}

	mode := defaultFileMode
	if info, statErr := root.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}

	out := restoreFormat(updated, hadBOM, hadCRLF)
	if err := atomicWriteRootFile(root, path, out, mode); err != nil {
		return EditResponse{}, err
	}
	return EditResponse{Replacements: replacements}, nil
}

func readBoundedRootFile(ctx context.Context, root *os.Root, path string, maxBytes int64) (_ []byte, err error) {
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	file, info, err := openRegularRootFile(ctx, root, path)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("%w: %s uses %d bytes; limit is %d", ErrFileTooLarge, path, info.Size(), maxBytes)
	}
	source := io.LimitReader(contextReader{ctx: ctx, reader: file}, maxBytes+1)
	data, err := io.ReadAll(source)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: %s grew beyond %d bytes", ErrFileTooLarge, path, maxBytes)
	}
	return data, nil
}

func detectRootFormat(ctx context.Context, root *os.Root, path string) (hadBOM, hadCRLF bool, err error) {
	file, _, err := openRegularRootFile(ctx, root, path)
	if err != nil {
		return false, false, err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	prefix, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: file}, formatDetectionBytes))
	if err != nil {
		return false, false, err
	}
	return bytes.HasPrefix(prefix, []byte(utf8BOM)), bytes.Contains(prefix, []byte("\r\n")), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (c contextReader) Read(buffer []byte) (int, error) {
	if cause := context.Cause(c.ctx); cause != nil {
		return 0, cause
	}
	read, err := c.reader.Read(buffer)
	if cause := context.Cause(c.ctx); cause != nil {
		return read, cause
	}
	return read, err
}

func (e editOperation) apply(content, path string) (string, int, error) {
	if e.OldString == "" {
		return "", 0, errors.New("old_string must not be empty")
	}
	occurrences := strings.Count(content, e.OldString)
	switch {
	case occurrences == 0:
		// Exact match failed — fall back to a whitespace-tolerant match so a
		// snippet that drifted on indentation / trailing whitespace still edits,
		// but ONLY when it's unambiguous: a near-match that hits several regions
		// is refused, never guessed (a wrong edit is worse than a clear failure).
		start, end, matches := fuzzyEditRegion(content, e.OldString)
		switch matches {
		case 0:
			return "", 0, fmt.Errorf("old_string not found in %s", path)
		case 1:
			return content[:start] + e.NewString + content[end:], 1, nil
		default:
			return "", 0, fmt.Errorf("old_string not found exactly in %s; %d regions match apart from whitespace — copy it verbatim (or add surrounding lines to disambiguate)", path, matches)
		}
	case occurrences > 1 && !e.ReplaceAll:
		return "", 0, fmt.Errorf("old_string matches %d times in %s — set replace_all=true to confirm", occurrences, path)
	default:
		n := 1
		if e.ReplaceAll {
			n = -1
		}
		replacements := occurrences
		if !e.ReplaceAll {
			replacements = 1
		}
		return strings.Replace(content, e.OldString, e.NewString, n), replacements, nil
	}
}

// binarySniffLen matches git's heuristic — a NUL in the first 8 KiB
// means the file is treated as binary.
const binarySniffLen = 8192

func looksBinary(data []byte) bool {
	sniff := data
	if len(sniff) > binarySniffLen {
		sniff = sniff[:binarySniffLen]
	}
	return bytes.IndexByte(sniff, 0) >= 0
}
