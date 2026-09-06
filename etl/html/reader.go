package html

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/andybalholm/cascadia"
	"github.com/samber/lo"
	htmlnode "golang.org/x/net/html"

	"github.com/Tangerg/scope/core/document"
	coremetadata "github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/etl"
)

// These keys name the reader-derived metadata attached to every emitted
// document, so a downstream splitter or retriever can rely on them being
// present rather than re-deriving them from content.
const (
	MetadataTitle       = "html.title"
	MetadataDescription = "html.description"
	MetadataCanonical   = "html.canonical"
	MetadataSelector    = "html.selector"
	MetadataSourceName  = "html.source"
)

// ReaderConfig controls HTML extraction. By default whitespace runs are collapsed;
// PreserveWhitespace retains the source spacing instead. Metadata is cloned
// by NewReader, and reader-derived html.* keys take precedence on conflict. A
// zero SourceBudget uses [etl.DefaultMaxSourceBytes].
type ReaderConfig struct {
	Selector           string
	SourceName         string
	PreserveWhitespace bool
	Metadata           coremetadata.Map
	SourceBudget       etl.SourceBudget
}

// Reader extracts documents from HTML.
type Reader struct {
	source          io.Reader
	selector        string
	matcher         goquery.Matcher
	sourceName      string
	stripWhitespace bool
	extraMetadata   coremetadata.Map
	sourceBudget    etl.SourceBudget
}

// NewReader compiles the selector at construction so an invalid one fails
// where it is configured rather than on every document. Unlike PDF, the budget
// is enforced while reading because an [io.Reader] cannot report its length in
// advance.
func NewReader(source io.Reader, config ReaderConfig) (*Reader, error) {
	if lo.IsNil(source) {
		return nil, errors.New("html reader: source must not be nil")
	}
	r := &Reader{
		source:          source,
		selector:        config.Selector,
		sourceName:      config.SourceName,
		stripWhitespace: !config.PreserveWhitespace,
		extraMetadata:   config.Metadata.Clone(),
		sourceBudget:    config.SourceBudget,
	}
	if err := r.extraMetadata.Validate(); err != nil {
		return nil, fmt.Errorf("html reader: invalid metadata: %w", err)
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

// Read parses the source and emits documents according to the configuration.
// Context cancellation is honored around parsing and between matches.
func (r *Reader) Read(ctx context.Context) ([]*document.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := r.sourceBudget.ReadAll(ctx, r.source)
	if err != nil {
		return nil, fmt.Errorf("html: read source: %w", err)
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("html: parse: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	page := newPageInfo(doc)

	if r.selector == "" {
		return r.readWhole(ctx, doc, page)
	}
	return r.readSelector(ctx, doc, page)
}

func (r *Reader) readWhole(ctx context.Context, doc *goquery.Document, page pageInfo) ([]*document.Document, error) {
	body := r.extractText(doc.Selection)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if body == "" {
		return nil, nil
	}
	d, err := r.buildDocument(body, page, "")
	if err != nil {
		return nil, fmt.Errorf("html: build document: %w", err)
	}
	return []*document.Document{d}, nil
}

func (r *Reader) readSelector(ctx context.Context, doc *goquery.Document, page pageInfo) ([]*document.Document, error) {
	var (
		docs     []*document.Document
		buildErr error
	)
	doc.FindMatcher(r.matcher).EachWithBreak(func(_ int, sel *goquery.Selection) bool {
		if ctx.Err() != nil {
			return false // cancellation is reported after the loop
		}
		body := r.extractText(sel)
		if body == "" {
			return true
		}
		d, err := r.buildDocument(body, page, r.selector)
		if err != nil {
			buildErr = err
			return false
		}
		docs = append(docs, d)
		return true
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if buildErr != nil {
		return nil, fmt.Errorf("html: build selector document: %w", buildErr)
	}
	return docs, nil
}

func (r *Reader) buildDocument(body string, page pageInfo, selector string) (*document.Document, error) {
	doc, err := document.NewDocument(body, nil)
	if err != nil {
		return nil, err
	}
	doc.Metadata, err = r.buildMetadata(page, selector)
	if err != nil {
		return nil, fmt.Errorf("encode metadata: %w", err)
	}
	return doc, nil
}

func (r *Reader) buildMetadata(page pageInfo, selector string) (coremetadata.Map, error) {
	md := r.extraMetadata.Clone()
	if err := page.applyTo(&md); err != nil {
		return nil, err
	}
	if r.sourceName != "" {
		if err := md.Set(MetadataSourceName, r.sourceName); err != nil {
			return nil, err
		}
	}
	if selector != "" {
		if err := md.Set(MetadataSelector, selector); err != nil {
			return nil, err
		}
	}
	if len(md) == 0 {
		return nil, nil
	}
	return md, nil
}

func (r *Reader) extractText(sel *goquery.Selection) string {
	var content strings.Builder
	for _, node := range sel.Nodes {
		appendNodeText(&content, node)
	}
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
	if node.Type == htmlnode.ElementNode {
		switch node.Data {
		case "script", "style", "noscript", "template", "head":
			return
		}
	}
	boundary := node.Type == htmlnode.ElementNode && textBoundaryElement(node.Data)
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

// Inline markup must not insert spaces inside words; structural boundaries must.
func textBoundaryElement(name string) bool {
	switch name {
	case "address", "article", "aside", "blockquote", "br", "caption", "dd", "details",
		"dialog", "div", "dl", "dt", "fieldset", "figcaption", "figure", "footer", "form",
		"h1", "h2", "h3", "h4", "h5", "h6", "header", "hgroup", "hr", "li", "main",
		"nav", "ol", "p", "pre", "section", "summary", "table", "tbody", "td", "tfoot",
		"th", "thead", "tr", "ul":
		return true
	default:
		return false
	}
}

type pageInfo struct {
	title       string
	description string
	canonical   string
}

func newPageInfo(doc *goquery.Document) pageInfo {
	var p pageInfo
	p.title = strings.TrimSpace(doc.Find("head > title").First().Text())

	doc.Find(`head > meta[name="description"]`).Each(func(_ int, s *goquery.Selection) {
		if c, ok := s.Attr("content"); ok && p.description == "" {
			p.description = strings.TrimSpace(c)
		}
	})
	doc.Find(`head > link[rel="canonical"]`).Each(func(_ int, s *goquery.Selection) {
		if href, ok := s.Attr("href"); ok && p.canonical == "" {
			p.canonical = strings.TrimSpace(href)
		}
	})
	return p
}

func (p pageInfo) applyTo(metadata *coremetadata.Map) error {
	for _, field := range [...]struct {
		key   string
		value string
	}{
		{MetadataTitle, p.title},
		{MetadataDescription, p.description},
		{MetadataCanonical, p.canonical},
	} {
		if field.value != "" {
			if err := metadata.Set(field.key, field.value); err != nil {
				return err
			}
		}
	}
	return nil
}
