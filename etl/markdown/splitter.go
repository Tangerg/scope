package markdown

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/samber/lo"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extensionast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/tokenizer"
	"github.com/Tangerg/scope/etl"
	"github.com/Tangerg/scope/etl/internal/tokenwindow"
)

const (
	defaultMaxTokensPerChunk = 800
	defaultMaxChunks         = 10_000
	minimumTableLines        = 3
	minimumCodeFenceLength   = 3
)

// ErrSemanticUnitTooLarge reports a Markdown unit that cannot be split without
// losing its semantic boundary.
var ErrSemanticUnitTooLarge = errors.New("markdown splitter: semantic unit exceeds token limit")

// SplitterConfig configures structure-aware Markdown chunking. Zero limits use
// documented defaults; negative limits are rejected.
type SplitterConfig struct {
	Tokenizer tokenizer.Tokenizer

	MaxTokensPerChunk int
	MaxChunks         int
	// MaxSearchWork bounds each paragraph prefix search in bytes rendered or
	// encoded plus tokens decoded; zero uses etl.DefaultMaxSearchWork.
	MaxSearchWork int
	IDGenerator   etl.IDGenerator
}

// Splitter produces token-bounded Markdown chunks without severing tables,
// list items, or code lines. Active heading ancestry is repeated in each chunk
// so independently retrieved chunks retain their section context.
type Splitter struct {
	parser            goldmark.Markdown
	tokenizer         tokenizer.Tokenizer
	maxTokensPerChunk int
	maxChunks         int
	maxSearchWork     int
	base              *etl.Splitter
}

func NewSplitter(config SplitterConfig) (*Splitter, error) {
	if lo.IsNil(config.Tokenizer) {
		return nil, errors.New("markdown splitter: tokenizer is required")
	}
	if config.MaxTokensPerChunk < 0 || config.MaxChunks < 0 || config.MaxSearchWork < 0 {
		return nil, errors.New("markdown splitter: limits must not be negative")
	}
	if config.MaxTokensPerChunk == 0 {
		config.MaxTokensPerChunk = defaultMaxTokensPerChunk
	}
	if config.MaxSearchWork == 0 {
		config.MaxSearchWork = etl.DefaultMaxSearchWork
	}
	if config.MaxChunks == 0 {
		config.MaxChunks = defaultMaxChunks
	}

	splitter := &Splitter{
		parser:            goldmark.New(goldmark.WithExtensions(extension.Table)),
		tokenizer:         config.Tokenizer,
		maxTokensPerChunk: config.MaxTokensPerChunk,
		maxChunks:         config.MaxChunks,
		maxSearchWork:     config.MaxSearchWork,
	}
	base, err := etl.NewSplitter(etl.SplitterConfig{
		SplitFunc:   splitter.SplitText,
		IDGenerator: config.IDGenerator,
	})
	if err != nil {
		return nil, err
	}
	splitter.base = base
	return splitter, nil
}

// SplitText splits one Markdown source string. Every returned chunk is
// non-empty and within MaxTokensPerChunk. The total is bounded by MaxChunks.
func (s *Splitter) SplitText(ctx context.Context, source string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !utf8.ValidString(source) {
		return nil, fmt.Errorf("markdown splitter: %w: source is not valid UTF-8", etl.ErrInvalidTextEncoding)
	}
	if strings.TrimSpace(source) == "" {
		return nil, nil
	}

	sections, err := s.parseSections(ctx, []byte(source))
	if err != nil {
		return nil, err
	}
	chunks := make([]string, 0, min(len(sections), s.maxChunks))
	for _, section := range sections {
		sectionChunks, err := s.splitSection(ctx, section)
		if err != nil {
			return nil, err
		}
		if len(chunks)+len(sectionChunks) > s.maxChunks {
			return nil, fmt.Errorf("%w: maximum is %d", etl.ErrChunkLimitExceeded, s.maxChunks)
		}
		chunks = append(chunks, sectionChunks...)
	}
	return chunks, nil
}

// Split preserves source metadata and stamps standard chunk-lineage
// metadata through the base ETL splitter.
func (s *Splitter) Split(ctx context.Context, docs []*document.Document) ([]*document.Document, error) {
	return s.base.Split(ctx, docs)
}

type blockKind uint8

const (
	blockParagraph blockKind = iota
	blockTable
	blockList
	blockFencedCode
	blockIndentedCode
	blockAtomic
)

func (b blockKind) String() string {
	switch b {
	case blockParagraph:
		return "paragraph"
	case blockTable:
		return "table row"
	case blockList:
		return "list item"
	case blockFencedCode, blockIndentedCode:
		return "code line"
	default:
		return "block"
	}
}

type markdownBlock struct {
	kind  blockKind
	text  string
	parts []string
}

type markdownSection struct {
	headings []string
	blocks   []markdownBlock
}

type sectionBuilder struct {
	sections []markdownSection
	active   markdownSection
	path     headingPath
}

// openHeading emits a heading-only section only when the new heading closes
// it; a deeper heading repeats the active path, so emitting it would duplicate.
func (s *sectionBuilder) openHeading(level int, title string) {
	if len(s.active.blocks) > 0 || s.path.closes(level) {
		s.sections = append(s.sections, s.active)
	}
	s.path.push(level, title)
	s.active = markdownSection{headings: s.path.titles()}
}

func (s *sectionBuilder) addBlock(block markdownBlock) {
	s.active.blocks = append(s.active.blocks, block)
}

func (s *sectionBuilder) finish() []markdownSection {
	if len(s.active.blocks) > 0 || len(s.active.headings) > 0 {
		s.sections = append(s.sections, s.active)
	}
	return s.sections
}

func (s *Splitter) parseSections(ctx context.Context, source []byte) ([]markdownSection, error) {
	root := s.parser.Parser().Parse(text.NewReader(source))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var builder sectionBuilder
	for node := root.FirstChild(); node != nil; node = node.NextSibling() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		start, end := nodeBounds(node, source)
		if start < 0 || end <= start {
			continue
		}
		raw := strings.Trim(string(source[start:end]), "\r\n")
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if heading, ok := node.(*ast.Heading); ok {
			builder.openHeading(heading.Level, raw)
			continue
		}
		builder.addBlock(classifyBlock(source, node, end, raw))
	}
	return builder.finish(), nil
}

func nodeBounds(node ast.Node, source []byte) (int, int) {
	start := node.Pos()
	if start >= 0 {
		// Goldmark positions may begin after indentation, which is part of a
		// Markdown block's syntax even when it is absent from its AST content.
		start = bytes.LastIndexByte(source[:start], '\n') + 1
	}
	end := len(source)
	if next := node.NextSibling(); next != nil && next.Pos() >= 0 {
		end = bytes.LastIndexByte(source[:next.Pos()], '\n') + 1
	}
	return start, end
}

func classifyBlock(source []byte, node ast.Node, end int, raw string) markdownBlock {
	block := markdownBlock{text: raw}
	switch typed := node.(type) {
	case *ast.Paragraph:
		block.kind = blockParagraph
	case *extensionast.Table:
		block.kind = blockTable
	case *ast.List:
		block.kind = blockList
		block.parts = childSources(source, typed, end)
	case *ast.FencedCodeBlock:
		block.kind = blockFencedCode
	case *ast.CodeBlock:
		block.kind = blockIndentedCode
	default:
		block.kind = blockAtomic
	}
	return block
}

func childSources(source []byte, parent ast.Node, parentEnd int) []string {
	parts := make([]string, 0, parent.ChildCount())
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		start := child.Pos()
		end := parentEnd
		if next := child.NextSibling(); next != nil && next.Pos() >= 0 {
			end = next.Pos()
		}
		if start < 0 || end <= start {
			continue
		}
		if value := strings.TrimSpace(string(source[start:end])); value != "" {
			parts = append(parts, value)
		}
	}
	return parts
}

func (s *Splitter) splitSection(ctx context.Context, section markdownSection) ([]string, error) {
	prefix := strings.Join(section.headings, "\n\n")
	if len(section.blocks) == 0 {
		if err := s.requireFits(ctx, blockAtomic, prefix); err != nil {
			return nil, err
		}
		return []string{prefix}, nil
	}

	var (
		chunks  []string
		current []string
	)
	flush := func() error {
		if len(current) == 0 {
			return nil
		}
		if len(chunks) == s.maxChunks {
			return fmt.Errorf("%w: maximum is %d", etl.ErrChunkLimitExceeded, s.maxChunks)
		}
		chunks = append(chunks, renderChunk(prefix, strings.Join(current, "\n\n")))
		current = nil
		return nil
	}

	for _, block := range section.blocks {
		parts, err := s.splitBlock(ctx, prefix, block)
		if err != nil {
			return nil, err
		}
		for _, part := range parts {
			candidate := slices.Concat(current, []string{part})
			fits, err := s.fits(ctx, renderChunk(prefix, strings.Join(candidate, "\n\n")))
			if err != nil {
				return nil, err
			}
			if fits {
				current = candidate
				continue
			}
			if err := flush(); err != nil {
				return nil, err
			}
			current = []string{part}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return chunks, nil
}

func (s *Splitter) splitBlock(ctx context.Context, prefix string, block markdownBlock) ([]string, error) {
	fits, err := s.fits(ctx, renderChunk(prefix, block.text))
	if err != nil {
		return nil, err
	}
	if fits {
		return []string{block.text}, nil
	}

	switch block.kind {
	case blockParagraph:
		return s.splitParagraph(ctx, prefix, block.text)
	case blockTable:
		return s.splitTable(ctx, prefix, block.text)
	case blockList:
		return s.groupSemanticUnits(ctx, prefix, block.kind, block.parts, func(items []string) string {
			return strings.Join(items, "\n\n")
		})
	case blockFencedCode:
		return s.splitFencedCode(ctx, prefix, block.text)
	case blockIndentedCode:
		lines := strings.Split(block.text, "\n")
		return s.groupSemanticUnits(ctx, prefix, block.kind, lines, func(lines []string) string {
			return strings.Join(lines, "\n")
		})
	default:
		return nil, s.semanticUnitError(ctx, block.kind, renderChunk(prefix, block.text))
	}
}

func (s *Splitter) splitParagraph(ctx context.Context, prefix, paragraph string) ([]string, error) {
	render := func(body string) string { return renderChunk(prefix, strings.TrimSpace(body)) }
	var chunks []string
	for paragraph = strings.TrimSpace(paragraph); paragraph != ""; {
		if len(chunks) == s.maxChunks {
			return nil, fmt.Errorf("%w: maximum is %d", etl.ErrChunkLimitExceeded, s.maxChunks)
		}
		decoded, err := tokenwindow.Prefix(ctx, s.tokenizer, paragraph, s.maxTokensPerChunk, s.maxSearchWork, render)
		if errors.Is(err, tokenwindow.ErrSearchBudgetExceeded) {
			return nil, etl.ErrSearchBudgetExceeded
		}
		if err != nil {
			return nil, fmt.Errorf("markdown splitter: select paragraph token window: %w", err)
		}
		if decoded == "" {
			_, size := utf8.DecodeRuneInString(paragraph)
			return nil, s.semanticUnitError(ctx, blockParagraph, render(paragraph[:size]))
		}
		paragraph = strings.TrimSpace(paragraph[len(decoded):])
		chunks = append(chunks, strings.TrimSpace(decoded))
	}
	return chunks, nil
}

func (s *Splitter) splitTable(ctx context.Context, prefix, table string) ([]string, error) {
	lines := nonEmptyLines(table)
	if len(lines) < minimumTableLines {
		return nil, s.semanticUnitError(ctx, blockTable, renderChunk(prefix, table))
	}
	header := lines[:2]
	return s.groupSemanticUnits(ctx, prefix, blockTable, lines[2:], func(rows []string) string {
		return strings.Join(slices.Concat(header, rows), "\n")
	})
}

func (s *Splitter) splitFencedCode(ctx context.Context, prefix, code string) ([]string, error) {
	lines := strings.Split(code, "\n")
	opening := lines[0]
	closing := closingFence(opening)
	content := lines[1:]
	if len(content) > 0 && isClosingFence(content[len(content)-1], opening) {
		closing = content[len(content)-1]
		content = content[:len(content)-1]
	}
	return s.groupSemanticUnits(ctx, prefix, blockFencedCode, content, func(lines []string) string {
		return opening + "\n" + strings.Join(lines, "\n") + "\n" + closing
	})
}

func (s *Splitter) groupSemanticUnits(
	ctx context.Context,
	prefix string,
	kind blockKind,
	units []string,
	render func([]string) string,
) ([]string, error) {
	if len(units) == 0 {
		body := render(nil)
		return nil, s.semanticUnitError(ctx, kind, renderChunk(prefix, body))
	}

	groups := make([]string, 0, min(len(units), s.maxChunks))
	current := make([]string, 0, len(units))
	for _, unit := range units {
		candidate := slices.Concat(current, []string{unit})
		body := render(candidate)
		fits, err := s.fits(ctx, renderChunk(prefix, body))
		if err != nil {
			return nil, err
		}
		if fits {
			current = candidate
			continue
		}
		if len(current) == 0 {
			return nil, s.semanticUnitError(ctx, kind, renderChunk(prefix, body))
		}
		if len(groups) == s.maxChunks {
			return nil, fmt.Errorf("%w: maximum is %d", etl.ErrChunkLimitExceeded, s.maxChunks)
		}
		groups = append(groups, render(current))
		current = []string{unit}
		body = render(current)
		fits, err = s.fits(ctx, renderChunk(prefix, body))
		if err != nil {
			return nil, err
		}
		if !fits {
			return nil, s.semanticUnitError(ctx, kind, renderChunk(prefix, body))
		}
	}
	if len(current) > 0 {
		if len(groups) == s.maxChunks {
			return nil, fmt.Errorf("%w: maximum is %d", etl.ErrChunkLimitExceeded, s.maxChunks)
		}
		groups = append(groups, render(current))
	}
	return groups, nil
}

func (s *Splitter) requireFits(ctx context.Context, kind blockKind, value string) error {
	fits, err := s.fits(ctx, value)
	if err != nil {
		return err
	}
	if fits {
		return nil
	}
	return s.semanticUnitError(ctx, kind, value)
}

func (s *Splitter) semanticUnitError(ctx context.Context, kind blockKind, value string) error {
	count, err := s.tokenCount(ctx, value)
	if err != nil {
		return err
	}
	return fmt.Errorf(
		"%w: %s requires %d tokens; maximum is %d",
		ErrSemanticUnitTooLarge,
		kind,
		count,
		s.maxTokensPerChunk,
	)
}

func (s *Splitter) fits(ctx context.Context, value string) (bool, error) {
	count, err := s.tokenCount(ctx, value)
	if err != nil {
		return false, err
	}
	return count <= s.maxTokensPerChunk, nil
}

func (s *Splitter) tokenCount(ctx context.Context, value string) (int, error) {
	tokens, err := s.tokenizer.Encode(ctx, value)
	if err != nil {
		return 0, fmt.Errorf("markdown splitter: measure chunk: %w", err)
	}
	return len(tokens), nil
}

func renderChunk(prefix, body string) string {
	prefix = strings.TrimSpace(prefix)
	body = strings.Trim(body, "\r\n")
	switch {
	case prefix == "":
		return body
	case body == "":
		return prefix
	default:
		return prefix + "\n\n" + body
	}
}

func nonEmptyLines(value string) []string {
	lines := strings.Split(value, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}
	return kept
}

func closingFence(opening string) string {
	trimmed := strings.TrimLeft(opening, " \t")
	indent := opening[:len(opening)-len(trimmed)]
	if trimmed == "" || (trimmed[0] != '`' && trimmed[0] != '~') {
		return "```"
	}
	return indent + strings.Repeat(string(trimmed[0]), max(minimumCodeFenceLength, fenceRun(trimmed)))
}

func isClosingFence(line, opening string) bool {
	open := strings.TrimLeft(opening, " \t")
	candidate := strings.TrimLeft(line, " \t")
	if open == "" || candidate == "" || candidate[0] != open[0] {
		return false
	}
	count := fenceRun(candidate)
	return count >= fenceRun(open) && strings.TrimSpace(candidate[count:]) == ""
}

// fenceRun counts the repeated fence character that begins a nonempty line.
func fenceRun(line string) int {
	count := 0
	for count < len(line) && line[count] == line[0] {
		count++
	}
	return count
}
