package markdown

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/samber/lo"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"

	"github.com/Tangerg/scope/core/document"
	coremetadata "github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/etl"
)

// Markdown metadata keys keep structural lineage explicit without changing the
// core document contract.
const (
	MetadataHeading      = "markdown.heading"
	MetadataHeadingLevel = "markdown.heading.level"
	MetadataHeadingPath  = "markdown.heading.path"
	MetadataSourceName   = "markdown.source"
)

// ReaderConfig controls Markdown extraction. HeadingSplitLevel emits one document
// per section split on headings at or above that level (1 = H1, 2 = H1+H2).
// Zero disables splitting; non-zero values must be in [1, 6]. Metadata is
// cloned by NewReader, and reader-derived markdown.* keys take precedence.
// A zero SourceBudget uses [etl.DefaultMaxSourceBytes].
type ReaderConfig struct {
	HeadingSplitLevel int
	SourceName        string
	Metadata          coremetadata.Map
	SourceBudget      etl.SourceBudget
}

// Reader extracts documents from Markdown.
type Reader struct {
	source            io.Reader
	parser            goldmark.Markdown
	headingSplitLevel int
	sourceName        string
	extraMetadata     coremetadata.Map
	sourceBudget      etl.SourceBudget
}

// NewReader freezes Markdown extraction and source-budget policy.
func NewReader(source io.Reader, config ReaderConfig) (*Reader, error) {
	if lo.IsNil(source) {
		return nil, errors.New("markdown reader: source must not be nil")
	}
	r := &Reader{
		source:            source,
		parser:            goldmark.New(),
		headingSplitLevel: config.HeadingSplitLevel,
		sourceName:        config.SourceName,
		extraMetadata:     config.Metadata.Clone(),
		sourceBudget:      config.SourceBudget,
	}
	if err := r.extraMetadata.Validate(); err != nil {
		return nil, fmt.Errorf("markdown reader: invalid metadata: %w", err)
	}
	if r.headingSplitLevel < 0 || r.headingSplitLevel > 6 {
		return nil, fmt.Errorf("markdown reader: heading split level %d is outside [1, 6]", r.headingSplitLevel)
	}
	return r, nil
}

// Read consumes the underlying reader and emits documents according to the
// configuration. Context cancellation is honored around parsing and between
// emitted sections.
func (r *Reader) Read(ctx context.Context) ([]*document.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := r.sourceBudget.ReadAll(ctx, r.source)
	if err != nil {
		return nil, fmt.Errorf("markdown: read source: %w", err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}

	if r.headingSplitLevel == 0 {
		return r.readWhole(raw)
	}
	sections, err := r.collectSections(ctx, raw)
	if err != nil {
		return nil, err
	}
	return r.sectionsToDocuments(ctx, sections)
}

// readWhole returns one document containing the entire markdown body.
// Blank input yields no documents — the same contract as the html and
// pdf readers, not an error.
func (r *Reader) readWhole(raw []byte) ([]*document.Document, error) {
	body := string(raw)
	if strings.TrimSpace(body) == "" {
		return nil, nil
	}
	doc, err := document.NewDocument(body, nil)
	if err != nil {
		return nil, fmt.Errorf("markdown: build document: %w", err)
	}
	doc.Metadata, err = r.baseMetadata()
	if err != nil {
		return nil, fmt.Errorf("markdown: encode metadata: %w", err)
	}
	return []*document.Document{doc}, nil
}

// collectSections walks the top-level AST nodes and groups them into
// sections: a new section opens at every heading of level
// <= headingSplitLevel, while the heading-path stack tracks ancestry.
// Body content before the first heading lands in an unnamed lead-in
// section.
func (r *Reader) collectSections(ctx context.Context, raw []byte) ([]*section, error) {
	root := r.parser.Parser().Parse(text.NewReader(raw))
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var (
		sections []*section
		path     headingPath
	)

	for n := root.FirstChild(); n != nil; n = n.NextSibling() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		heading, ok := n.(*ast.Heading)
		if !ok || heading.Level > r.headingSplitLevel {
			// Body content — attach to the most recent open section, or
			// create an unnamed lead-in section if none exists yet.
			if len(sections) == 0 {
				sections = append(sections, &section{})
			}
			sections[len(sections)-1].appendNodeSource(raw, n)
			continue
		}

		// New split-level heading: open a new section, manage the path stack.
		title := r.headingText(heading, raw)
		path.push(heading.Level, title)

		sec := &section{
			heading: title,
			level:   heading.Level,
			path:    path.String(),
		}
		sec.appendNodeSource(raw, n)
		sections = append(sections, sec)
	}
	return sections, nil
}

// sectionsToDocuments materializes each non-empty section into a
// [document.Document], stamping heading metadata when present. ctx
// cancellation is honored between sections.
func (r *Reader) sectionsToDocuments(ctx context.Context, sections []*section) ([]*document.Document, error) {
	var docs []*document.Document
	for _, sec := range sections {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		md, err := r.baseMetadata()
		if err != nil {
			return nil, fmt.Errorf("markdown: encode section metadata: %w", err)
		}
		doc, err := sec.document(md)
		if err != nil {
			return nil, fmt.Errorf("markdown: materialize section: %w", err)
		}
		if doc == nil {
			continue
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

// headingText collects all inline text descendants so formatting, links, and
// code spans do not disappear from heading metadata.
func (*Reader) headingText(h *ast.Heading, raw []byte) string {
	var b strings.Builder
	_ = ast.Walk(h, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch typed := node.(type) {
		case *ast.Text:
			b.Write(typed.Value(raw))
		case *ast.String:
			b.Write(typed.Value)
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

func (r *Reader) baseMetadata() (coremetadata.Map, error) {
	md := r.extraMetadata.Clone()
	if r.sourceName != "" {
		if err := md.Set(MetadataSourceName, r.sourceName); err != nil {
			return nil, err
		}
	}
	return md, nil
}

// section is the accumulated body of a single emitted document.
type section struct {
	heading string
	level   int
	path    string
	builder strings.Builder
}

type sectionRef struct {
	level int
	title string
}

type headingPath []sectionRef

func (h *headingPath) push(level int, title string) {
	for len(*h) > 0 && (*h)[len(*h)-1].level >= level {
		*h = (*h)[:len(*h)-1]
	}
	*h = append(*h, sectionRef{level: level, title: title})
}

func (h headingPath) String() string {
	titles := make([]string, len(h))
	for i, ref := range h {
		titles[i] = ref.title
	}
	return strings.Join(titles, " > ")
}

func (s *section) document(metadata coremetadata.Map) (*document.Document, error) {
	body := strings.TrimSpace(s.builder.String())
	if body == "" {
		return nil, nil
	}
	if s.heading != "" {
		if err := metadata.Set(MetadataHeading, s.heading); err != nil {
			return nil, err
		}
		if err := metadata.Set(MetadataHeadingLevel, s.level); err != nil {
			return nil, err
		}
		if err := metadata.Set(MetadataHeadingPath, s.path); err != nil {
			return nil, err
		}
	}
	doc, err := document.NewDocument(body, nil)
	if err != nil {
		return nil, err
	}
	doc.Metadata = metadata
	return doc, nil
}

// appendNodeSource copies the contiguous raw source range backing n. Using
// top-level node boundaries preserves Markdown syntax and all whitespace
// between adjacent nodes instead of reconstructing source from AST leaves.
func (s *section) appendNodeSource(raw []byte, n ast.Node) {
	start, end := nodeBounds(n, raw)
	if start < 0 || end <= start {
		return
	}
	s.builder.Write(raw[start:end])
}
