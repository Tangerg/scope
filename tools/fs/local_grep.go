package fs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

type ripgrepEventType string

const (
	ripgrepExecutable                         = "rg"
	ripgrepNoMatchesExitCode                  = 1
	ripgrepEventBegin        ripgrepEventType = "begin"
	ripgrepEventMatch        ripgrepEventType = "match"
	ripgrepEventContext      ripgrepEventType = "context"
	ripgrepEventEnd          ripgrepEventType = "end"
	ripgrepEventSummary      ripgrepEventType = "summary"
)

type grepSearch struct {
	ctx        context.Context
	root       *os.Root
	executable string
	args       []string
	filter     grepFileFilter
	decoder    *ripgrepDecoder
}

func newGrepSearch(ctx context.Context, root *os.Root, executable string, in GrepInput, decoder *ripgrepDecoder) (grepSearch, error) {
	filter, err := newGrepFileFilter(ctx, executable, in)
	if err != nil {
		return grepSearch{}, err
	}
	args := in.ripgrepArguments(decoder.mode)
	// An empty run rejects an invalid pattern even when no file is selected.
	if err := runRipgrep(ctx, executable, args, strings.NewReader(""), newRipgrepDecoder(decoder.mode, 1)); err != nil {
		return grepSearch{}, err
	}
	return grepSearch{ctx: ctx, root: root, executable: executable, args: args, filter: filter, decoder: decoder}, nil
}

func (g grepSearch) run(base string, directory bool) error {
	if !directory {
		return g.file(base)
	}
	walkRoot := filepath.ToSlash(base)
	return fs.WalkDir(globFilesystem{FS: g.root.FS(), ctx: g.ctx}, walkRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := g.ctx.Err(); err != nil {
			return err
		}
		if path != walkRoot && strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if err := g.file(filepath.FromSlash(path)); err != nil {
			return err
		}
		// Files are visited once, so a truncated response can admit nothing more.
		if g.decoder.response.Truncated {
			return fs.SkipAll
		}
		return nil
	})
}

func (g grepSearch) file(path string) (err error) {
	admitted, err := g.filter.admits(path)
	if err != nil || !admitted {
		return err
	}
	file, _, err := openRegularRootFile(g.ctx, g.root, path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	g.decoder.sourcePath = filepath.ToSlash(path)
	return runRipgrep(g.ctx, g.executable, g.args, file, g.decoder)
}

// runRipgrep feeds input to one ripgrep process and decodes its events into
// decoder. Files arrive on stdin because only os.Root may resolve their paths.
func runRipgrep(ctx context.Context, executable string, args []string, input io.Reader, decoder *ripgrepDecoder) error {
	command := exec.CommandContext(ctx, executable, args...)
	command.Stdin = input
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open ripgrep output: %w", err)
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("start ripgrep: %w", err)
	}
	if err := decoder.decode(stdout); err != nil {
		// The decode error is the result; killing and reaping are cleanup.
		_ = command.Process.Kill()
		_ = command.Wait()
		return err
	}
	waitErr := command.Wait()
	if waitErr == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](waitErr); ok && exitErr.ExitCode() == ripgrepNoMatchesExitCode {
		return nil
	}
	if message := strings.TrimSpace(stderr.String()); message != "" {
		return fmt.Errorf("%w: %s", waitErr, message)
	}
	return waitErr
}

// grepFileFilter applies Glob and FileType before a file reaches ripgrep,
// which sees only stdin and so cannot apply its own path filters.
type grepFileFilter struct {
	glob  string
	types []string
}

func newGrepFileFilter(ctx context.Context, executable string, in GrepInput) (grepFileFilter, error) {
	if in.Glob != "" {
		if err := validateGlobPattern(in.Glob); err != nil {
			return grepFileFilter{}, err
		}
	}
	filter := grepFileFilter{glob: in.Glob}
	if in.FileType == "" {
		return filter, nil
	}
	data, err := exec.CommandContext(ctx, executable, "--no-config", "--type-list").Output()
	if err != nil {
		return grepFileFilter{}, fmt.Errorf("read ripgrep file types: %w", err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if patterns, found := strings.CutPrefix(line, in.FileType+": "); found {
			filter.types = strings.Split(patterns, ", ")
			return filter, nil
		}
	}
	return grepFileFilter{}, fmt.Errorf("fs.Grep: unknown file type %q", in.FileType)
}

// A glob without a separator matches the file name, as in ripgrep; otherwise
// it matches the root-relative path.
func (g grepFileFilter) admits(path string) (bool, error) {
	if g.glob != "" {
		candidate := filepath.ToSlash(path)
		if !strings.Contains(g.glob, "/") {
			candidate = filepath.Base(path)
		}
		matched, err := doublestar.Match(g.glob, candidate)
		if err != nil || !matched {
			return false, err
		}
	}
	if len(g.types) == 0 {
		return true, nil
	}
	name := filepath.Base(path)
	return slices.ContainsFunc(g.types, func(pattern string) bool {
		matched, _ := doublestar.Match(pattern, name)
		return matched
	}), nil
}

type ripgrepText struct {
	Text  *string `json:"text"`
	Bytes *string `json:"bytes"`
}

func (r ripgrepText) decode() (string, error) {
	if r.Text != nil && r.Bytes != nil {
		return "", errors.New("ripgrep text contains both text and bytes")
	}
	if r.Text != nil {
		return *r.Text, nil
	}
	if r.Bytes == nil {
		return "", errors.New("ripgrep text has no representation")
	}
	decoded, err := base64.StdEncoding.DecodeString(*r.Bytes)
	if err != nil {
		return "", fmt.Errorf("decode ripgrep bytes: %w", err)
	}
	return string(decoded), nil
}

type ripgrepEvent struct {
	Type ripgrepEventType `json:"type"`
	Data ripgrepEventData `json:"data"`
}

type ripgrepEventData struct {
	Lines      ripgrepText `json:"lines"`
	LineNumber uint64      `json:"line_number"`
}

func (r ripgrepEventData) line() (int, error) {
	if r.LineNumber == 0 || r.LineNumber > uint64(math.MaxInt) {
		return 0, fmt.Errorf("ripgrep event contains invalid line number %d", r.LineNumber)
	}
	return int(r.LineNumber), nil
}

// ripgrepDecoder accumulates one bounded response across per-file ripgrep
// runs. ripgrep names stdin rather than the file, so sourcePath attributes
// each run's events to the root-relative path being searched.
type ripgrepDecoder struct {
	sourcePath string
	mode       GrepOutputMode
	maxResults int
	response   GrepResponse
	files      map[string]int
}

func newRipgrepDecoder(mode GrepOutputMode, maxResults int) *ripgrepDecoder {
	return &ripgrepDecoder{mode: mode, maxResults: maxResults, files: make(map[string]int)}
}

func (r *ripgrepDecoder) decode(reader io.Reader) error {
	decoder := jsontext.NewDecoder(reader)
	for {
		var event ripgrepEvent
		if err := jsonv2.UnmarshalDecode(decoder, &event); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("decode ripgrep event: %w", err)
		}
		if err := r.accept(event); err != nil {
			return err
		}
	}
}

func (r *ripgrepDecoder) accept(event ripgrepEvent) error {
	switch event.Type {
	case ripgrepEventBegin, ripgrepEventEnd, ripgrepEventSummary:
		return nil
	case ripgrepEventMatch, ripgrepEventContext:
		return r.acceptLine(event)
	default:
		return fmt.Errorf("unsupported ripgrep event type %q", event.Type)
	}
}

func (r *ripgrepDecoder) acceptLine(event ripgrepEvent) error {
	if r.sourcePath == "" {
		return errors.New("ripgrep reported a line without a source file")
	}
	line, err := event.Data.line()
	if err != nil {
		return err
	}
	match := event.Type == ripgrepEventMatch
	switch r.mode {
	case GrepOutputFilesWithMatches:
		if match {
			r.addFile(r.sourcePath)
		}
		return nil
	case GrepOutputCount:
		if match {
			r.addCount(r.sourcePath)
		}
		return nil
	}

	text, err := event.Data.Lines.decode()
	if err != nil {
		return fmt.Errorf("decode ripgrep line: %w", err)
	}
	kind := GrepLineContext
	if match {
		kind = GrepLineMatch
	}
	r.addLine(GrepLine{
		Path: r.sourcePath,
		Line: line,
		Text: strings.TrimSuffix(strings.TrimSuffix(text, "\n"), "\r"),
		Kind: kind,
	})
	return nil
}

func (r *ripgrepDecoder) addLine(line GrepLine) {
	if len(r.response.Lines) < r.maxResults {
		r.response.Lines = append(r.response.Lines, line)
		return
	}
	r.response.Truncated = true
}

func (r *ripgrepDecoder) addFile(path string) {
	if _, exists := r.files[path]; exists {
		return
	}
	if len(r.response.Files) >= r.maxResults {
		r.response.Truncated = true
		return
	}
	r.files[path] = len(r.response.Files)
	r.response.Files = append(r.response.Files, path)
}

func (r *ripgrepDecoder) addCount(path string) {
	if index, exists := r.files[path]; exists {
		r.response.Counts[index].Count++
		return
	}
	if len(r.response.Counts) >= r.maxResults {
		r.response.Truncated = true
		return
	}
	r.files[path] = len(r.response.Counts)
	r.response.Counts = append(r.response.Counts, GrepFileCount{Path: path, Count: 1})
}
