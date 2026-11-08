package html

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/andybalholm/cascadia"
	htmlnode "golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/Tangerg/scope/core/document"
	coremetadata "github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/etl"
	"github.com/Tangerg/scope/etl/internal/origin"
	etltext "github.com/Tangerg/scope/etl/text"
)

const (
	MetadataTitle       = "html.title"
	MetadataDescription = "html.description"
	MetadataCanonical   = "html.canonical"
	MetadataSelector    = "html.selector"
	hiddenAttribute     = "hidden"
)

// ReaderConfig controls HTML extraction. By default whitespace runs are collapsed;
// PreserveWhitespace retains the source spacing instead. Metadata is cloned
// by NewReader and cannot contain the reader's title, description, canonical,
// or selector keys. A zero SourceBudget uses [etl.DefaultMaxSourceBytes].
type ReaderConfig struct {
	Selector           string
	SourceName         string
	PreserveWhitespace bool
	Metadata           coremetadata.Map
	SourceBudget       etl.SourceBudget
}

// Reader parses the sequential input consumed by [etltext.Reader]. Reopen the
// original source for a new attempt; cancellation before reading leaves it intact.
type Reader struct {
	source          *etltext.Reader
	selector        string
	matcher         goquery.Matcher
	stripWhitespace bool
	extraMetadata   coremetadata.Map
}

// NewReader validates the selector without consuming source. The byte budget is
// enforced by Read because io.Reader does not provide a source length.
func NewReader(source io.Reader, config ReaderConfig) (*Reader, error) {
	input, err := etltext.NewReader(source, etltext.ReaderConfig{SourceName: config.SourceName, SourceBudget: config.SourceBudget})
	if err != nil {
		return nil, fmt.Errorf("html reader: %w", err)
	}
	r := &Reader{
		source:          input,
		selector:        config.Selector,
		stripWhitespace: !config.PreserveWhitespace,
		extraMetadata:   config.Metadata.Clone(),
	}
	if err := r.extraMetadata.Validate(); err != nil {
		return nil, fmt.Errorf("html reader: invalid metadata: %w", err)
	}
	for _, key := range [...]string{MetadataTitle, MetadataDescription, MetadataCanonical, MetadataSelector} {
		if _, exists := r.extraMetadata[key]; exists {
			return nil, fmt.Errorf("html reader: metadata key %q is owned by the reader", key)
		}
	}
	if r.selector != "" {
		matcher, err := cascadia.Compile(r.selector)
		if err != nil {
			return nil, fmt.Errorf("html reader: invalid selector %q: %w", r.selector, err)
		}
		r.matcher = matcher
	}
	return r, nil
}

func (r *Reader) Read(ctx context.Context) ([]*document.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	input, err := r.source.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("html: read source: %w", err)
	}
	if len(input) == 0 {
		return nil, nil
	}
	// HTML tokenization consumes UTF-8. The text reader owns the bounded
	// stream and original revision; rendered DOM text has no proven byte span.
	p := input[0].Provenance.Excerpt()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(input[0].Text))
	if err != nil {
		return nil, fmt.Errorf("html: parse: %w", err)
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	selection := doc.Selection
	if r.matcher != nil {
		selection = doc.FindMatcher(r.matcher)
	}
	facts, err := r.pageMetadata(doc)
	if err != nil {
		return nil, fmt.Errorf("html: page metadata: %w", err)
	}
	var docs []*document.Document
	for _, node := range selection.Nodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		body := r.extractText(node)
		if body == "" {
			continue
		}
		d, buildErr := document.NewDocument(body, nil)
		if buildErr != nil {
			return nil, fmt.Errorf("html: build document: %w", buildErr)
		}
		d.Metadata = facts.Clone()
		docs = append(docs, d)
	}
	if r.matcher != nil {
		return origin.Parts(ctx, p, docs)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(docs) > 0 {
		docs[0].Provenance = p
	}
	return docs, nil
}

func (r *Reader) extractText(node *htmlnode.Node) string {
	for ancestor := node.Parent; ancestor != nil; ancestor = ancestor.Parent {
		if excludesText(ancestor) {
			return ""
		}
	}
	var content strings.Builder
	appendNodeText(&content, node)
	text := content.String()
	if r.stripWhitespace {
		text = strings.Join(strings.Fields(text), " ")
	}
	return strings.TrimSpace(text)
}

func appendNodeText(content *strings.Builder, node *htmlnode.Node) {
	if node.Type == htmlnode.TextNode {
		content.WriteString(node.Data)
		return
	}
	if excludesText(node) {
		return
	}
	boundary := node.Type == htmlnode.ElementNode && textBoundaryElement(node.DataAtom)
	if boundary {
		content.WriteByte('\n')
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		appendNodeText(content, child)
	}
	if boundary {
		content.WriteByte('\n')
	}
}

func excludesText(node *htmlnode.Node) bool {
	if node.Type != htmlnode.ElementNode {
		return false
	}
	switch node.DataAtom {
	case atom.Script, atom.Style, atom.Noscript, atom.Template, atom.Head:
		return true
	}
	for _, attribute := range node.Attr {
		if attribute.Namespace == "" && attribute.Key == hiddenAttribute {
			return true
		}
	}
	return false
}

// Inline markup must not insert spaces inside words; structural boundaries must.
func textBoundaryElement(name atom.Atom) bool {
	switch name {
	case atom.Address, atom.Article, atom.Aside, atom.Blockquote, atom.Br, atom.Caption, atom.Dd, atom.Details,
		atom.Dialog, atom.Div, atom.Dl, atom.Dt, atom.Fieldset, atom.Figcaption, atom.Figure, atom.Footer, atom.Form,
		atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Header, atom.Hgroup, atom.Hr, atom.Li, atom.Main,
		atom.Nav, atom.Ol, atom.P, atom.Pre, atom.Section, atom.Summary, atom.Table, atom.Tbody, atom.Td, atom.Tfoot,
		atom.Th, atom.Thead, atom.Tr, atom.Ul:
		return true
	default:
		return false
	}
}

func (r *Reader) pageMetadata(doc *goquery.Document) (coremetadata.Map, error) {
	title := strings.TrimSpace(doc.Find("head > title").First().Text())
	var description, canonical string
	doc.Find(`head > meta[name="description"]`).Each(func(_ int, s *goquery.Selection) {
		if c, ok := s.Attr("content"); ok && description == "" {
			description = strings.TrimSpace(c)
		}
	})
	// rel is a case-insensitive token list, so "canonical" may share it with other link types.
	doc.Find(`head > link[rel~="canonical" i]`).Each(func(_ int, s *goquery.Selection) {
		if href, ok := s.Attr("href"); ok && canonical == "" {
			canonical = strings.TrimSpace(href)
		}
	})
	facts := r.extraMetadata.Clone()
	for _, field := range [...]struct{ key, value string }{
		{MetadataTitle, title}, {MetadataDescription, description}, {MetadataCanonical, canonical}, {MetadataSelector, r.selector},
	} {
		if field.value != "" {
			if err := facts.Set(field.key, field.value); err != nil {
				return nil, err
			}
		}
	}
	if len(facts) == 0 {
		return nil, nil
	}
	return facts, nil
}
