package pdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	coremetadata "github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/etl"
	"github.com/Tangerg/scope/etl/internal/origin"
	"github.com/Tangerg/scope/etl/pdf/internal/parser"
)

const (
	MetadataPagesTotal = "pdf.pages.total"

	DefaultMaxPages = 10_000
)

// ErrPartialRead accompanies usable output when one or more pages could not be read.
var ErrPartialRead = errors.New("pdf reader: one or more pages could not be read")

// ErrPageLimit prevents an untrusted page count from driving allocation or work.
var ErrPageLimit = errors.New("pdf reader: page limit exceeded")

var ErrInvalidPassword = parser.ErrInvalidPassword

// ReaderConfig controls PDF extraction. PerPage emits one document per readable
// page. Metadata is cloned by NewReader,
// and cannot contain the reader's page total key. A zero
// SourceBudget uses [etl.DefaultMaxSourceBytes]. A zero MaxPages uses
// [DefaultMaxPages], independently of the encoded source's byte size.
type ReaderConfig struct {
	PerPage    bool
	SourceName string
	// Revisions 2–4 derive keys from bytes. Accepting encoded bytes avoids
	// guessing the text encoding used by the document's producer.
	UserPassword []byte
	Metadata     coremetadata.Map
	SourceBudget etl.SourceBudget
	MaxPages     int
	ParseBudget  ParseBudget
}

type Reader struct {
	source        io.ReaderAt
	size          int64
	perPage       bool
	sourceName    string
	sourceBudget  etl.SourceBudget
	userPassword  []byte
	extraMetadata coremetadata.Map
	maxPages      int
	parseLimits   parser.Limits
}

// NewReader rejects an oversized source here instead of while extracting,
// because the size is known up front and a budget enforced mid-parse would have
// already allocated the pages it was meant to prevent. The [io.ReaderAt] and
// explicit size are required by the PDF format itself, which is read by random
// access from a trailer rather than streamed.
func NewReader(source io.ReaderAt, size int64, config ReaderConfig) (*Reader, error) {
	if lo.IsNil(source) {
		return nil, errors.New("pdf reader: source must not be nil")
	}
	if size <= 0 {
		return nil, errors.New("pdf reader: size must be positive")
	}
	if size > config.SourceBudget.MaxBytes() {
		return nil, fmt.Errorf(
			"pdf reader: %w: source is %d bytes; maximum is %d bytes",
			etl.ErrSourceTooLarge,
			size,
			config.SourceBudget.MaxBytes(),
		)
	}
	if config.MaxPages < 0 {
		return nil, errors.New("pdf reader: maximum pages must not be negative")
	}
	maxPages := config.MaxPages
	if maxPages == 0 {
		maxPages = DefaultMaxPages
	}
	limits, err := config.ParseBudget.limits()
	if err != nil {
		return nil, err
	}
	r := &Reader{
		source:     source,
		size:       size,
		perPage:    config.PerPage,
		sourceName: config.SourceName, sourceBudget: config.SourceBudget,
		userPassword:  bytes.Clone(config.UserPassword),
		extraMetadata: config.Metadata.Clone(),
		maxPages:      maxPages,
		parseLimits:   limits,
	}
	if err := r.extraMetadata.Validate(); err != nil {
		return nil, fmt.Errorf("pdf reader: invalid metadata: %w", err)
	}
	for _, key := range [...]string{MetadataPagesTotal} {
		if _, exists := r.extraMetadata[key]; exists {
			return nil, fmt.Errorf("pdf reader: metadata key %q is owned by the reader", key)
		}
	}
	return r, nil
}

// Read skips pages that fail to parse and reports them through
// [ErrPartialRead] alongside the documents that were decoded. A document-level
// parse failure, cancellation or exhausted ParseBudget returns no documents.
func (r *Reader) Read(ctx context.Context) (docs []*document.Document, err error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	// The parser reports malformed trailers and xref tables by panicking, and
	// only PlainText recovers internally, so a corrupt PDF would otherwise
	// crash the caller.
	defer func() {
		if rec := recover(); rec != nil {
			if cause, ok := rec.(error); ok {
				docs, err = nil, fmt.Errorf("pdf: parse: %w", cause)
			} else {
				docs, err = nil, fmt.Errorf("pdf: malformed document: %v", rec)
			}
		}
	}()
	var p *document.Provenance
	source := r.source
	if r.sourceName != "" {
		// Parse the same bounded snapshot that was hashed. A second pass over a
		// mutable ReaderAt cannot attest that the pages came from those bytes.
		raw, originInfo, readErr := origin.Read(ctx, io.NewSectionReader(source, 0, r.size), r.sourceBudget, r.sourceName)
		if readErr != nil {
			return nil, readErr
		}
		if int64(len(raw)) != r.size {
			return nil, io.ErrUnexpectedEOF
		}
		source = bytes.NewReader(raw)
		p = originInfo
	}
	pdfReader, err := r.openReader(ctx, source)
	if err != nil {
		return nil, err
	}

	total := pdfReader.NumPage()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if total < 0 {
		return nil, errors.New("pdf: malformed document: negative page count")
	}
	if total > r.maxPages {
		return nil, fmt.Errorf("%w: declared %d pages; maximum is %d", ErrPageLimit, total, r.maxPages)
	}
	if r.perPage {
		return r.readPages(ctx, pdfReader, total, p)
	}
	return r.readWhole(ctx, pdfReader, total, p)
}

func (r *Reader) openReader(ctx context.Context, source io.ReaderAt) (*parser.Reader, error) {
	pdfReader, err := parser.NewReader(ctx, source, r.size, r.parseLimits, r.userPassword)
	if err != nil {
		return nil, fmt.Errorf("pdf: open: %w", err)
	}
	return pdfReader, nil
}

func (r *Reader) readWhole(ctx context.Context, pdfReader *parser.Reader, total int, p *document.Provenance) ([]*document.Document, error) {
	body, readErr := r.readAllText(ctx, pdfReader, total)
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, readErr
	}
	doc, err := document.NewDocument(body, nil)
	if err != nil {
		return nil, fmt.Errorf("pdf: build document: %w", err)
	}
	doc.Metadata, err = r.baseMetadata(total)
	if err != nil {
		return nil, fmt.Errorf("pdf: encode metadata: %w", err)
	}
	doc.Provenance = p
	return []*document.Document{doc}, readErr
}

func (r *Reader) readPages(ctx context.Context, pdfReader *parser.Reader, total int, p *document.Provenance) ([]*document.Document, error) {
	docs := make([]*document.Document, 0, total)
	var failures pageErrors
	for index := range total {
		page := index + 1
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		text, err := r.pageText(pdfReader, page)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if err != nil {
			if errors.Is(err, ErrParseBudget) {
				return nil, err
			}
			failures = append(failures, err)
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		doc, err := document.NewDocument(text, nil)
		if err != nil {
			return nil, fmt.Errorf("pdf: page %d build: %w", page, err)
		}
		md, err := r.baseMetadata(total)
		if err != nil {
			return nil, fmt.Errorf("pdf: page %d metadata: %w", page, err)
		}
		doc.Provenance = p.Clone()
		if doc.Provenance == nil {
			doc.Provenance = &document.Provenance{}
		}
		doc.Provenance.Page = page
		doc.Metadata = md
		docs = append(docs, doc)
	}
	return docs, failures.err()
}

func (r *Reader) baseMetadata(total int) (coremetadata.Map, error) {
	md := r.extraMetadata.Clone()
	if err := md.Set(MetadataPagesTotal, total); err != nil {
		return nil, err
	}
	return md, nil
}

// The per-page API is used instead of Reader.PlainText so one bad page is
// skipped rather than aborting the whole document.
func (r *Reader) readAllText(ctx context.Context, pdfReader *parser.Reader, total int) (string, error) {
	var b strings.Builder
	var failures pageErrors
	for index := range total {
		page := index + 1
		if err := ctx.Err(); err != nil {
			return "", err
		}
		text, err := r.pageText(pdfReader, page)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if err != nil {
			if errors.Is(err, ErrParseBudget) {
				return "", err
			}
			failures = append(failures, err)
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(text)
	}
	return b.String(), failures.err()
}

type pageErrors []error

func (p pageErrors) err() error {
	if len(p) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrPartialRead, errors.Join(p...))
}

// Page and object resolution panic on malformed content outside
// PlainText's own recovery, so a bad page becomes a skippable error here.
func (*Reader) pageText(pdfReader *parser.Reader, pageIndex int) (text string, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			if cause, ok := rec.(error); ok {
				text, err = "", fmt.Errorf("page %d: %w", pageIndex, cause)
			} else {
				text, err = "", fmt.Errorf("page %d: malformed page: %v", pageIndex, rec)
			}
		}
	}()
	page := pdfReader.Page(pageIndex)
	if page.V.IsNull() {
		return "", fmt.Errorf("page %d: declared page is missing", pageIndex)
	}
	text, err = page.PlainText()
	if err != nil {
		return "", fmt.Errorf("page %d: %w", pageIndex, err)
	}
	return text, nil
}
