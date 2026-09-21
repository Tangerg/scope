package pdf

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	ledongthuc "github.com/ledongthuc/pdf"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	coremetadata "github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/etl"
)

// These keys name the reader-derived metadata attached to every emitted
// document, so a downstream splitter or retriever can rely on them being
// present rather than re-deriving them from content.
const (
	MetadataPageIndex  = "pdf.page"
	MetadataPagesTotal = "pdf.pages.total"
	MetadataSourceName = "pdf.source"
)

// ErrPartialRead accompanies usable output when one or more pages could not be read.
var ErrPartialRead = errors.New("pdf reader: one or more pages could not be read")

// ReaderConfig controls PDF extraction. PerPage emits one document per readable
// page. Password is used for encrypted PDFs. Metadata is cloned by NewReader,
// and reader-derived pdf.* keys take precedence on conflict. A zero
// SourceBudget uses [etl.DefaultMaxSourceBytes].
type ReaderConfig struct {
	PerPage      bool
	SourceName   string
	Password     string
	Metadata     coremetadata.Map
	SourceBudget etl.SourceBudget
}

// Reader extracts documents from PDF.
type Reader struct {
	source        io.ReaderAt
	size          int64
	perPage       bool
	sourceName    string
	password      string
	extraMetadata coremetadata.Map
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
	r := &Reader{
		source:        source,
		size:          size,
		perPage:       config.PerPage,
		sourceName:    config.SourceName,
		password:      config.Password,
		extraMetadata: config.Metadata.Clone(),
	}
	if err := r.extraMetadata.Validate(); err != nil {
		return nil, fmt.Errorf("pdf reader: invalid metadata: %w", err)
	}
	return r, nil
}

// Read parses the source and emits documents according to the
// configuration. Context cancellation is honored between pages.
//
// Pages that fail to parse are skipped and reported through [ErrPartialRead];
// successfully decoded documents are returned alongside that error. A
// document-level parse failure returns no documents. Both guard against the
// upstream library's panic-on-malformed-input style.
func (r *Reader) Read(ctx context.Context) (docs []*document.Document, err error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	// ledongthuc/pdf (following rsc/pdf) reports malformed input by
	// panicking deep in the parser, and only its GetPlainText path
	// recovers internally. Convert document-level panics (trailer /
	// xref parsing in ledongthuc.NewReader and NumPage) into errors at the module
	// boundary so a corrupt PDF can't crash the caller.
	defer func() {
		if rec := recover(); rec != nil {
			docs, err = nil, fmt.Errorf("pdf: malformed document: %v", rec)
		}
	}()
	pdfReader, err := r.openReader(ctx)
	if err != nil {
		return nil, err
	}

	total := pdfReader.NumPage()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.perPage {
		return r.readPages(ctx, pdfReader, total)
	}
	return r.readWhole(ctx, pdfReader, total)
}

func (r *Reader) openReader(ctx context.Context) (*ledongthuc.Reader, error) {
	if r.password != "" {
		password := r.password
		pdfReader, err := ledongthuc.NewReaderEncrypted(r.source, r.size, func() string {
			if ctx.Err() != nil {
				return ""
			}
			attempt := password
			password = ""
			return attempt
		})
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if err != nil {
			return nil, fmt.Errorf("pdf: open encrypted: %w", err)
		}
		return pdfReader, nil
	}
	pdfReader, err := ledongthuc.NewReader(r.source, r.size)
	if err != nil {
		return nil, fmt.Errorf("pdf: open: %w", err)
	}
	return pdfReader, nil
}

func (r *Reader) readWhole(ctx context.Context, pdfReader *ledongthuc.Reader, total int) ([]*document.Document, error) {
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
	return []*document.Document{doc}, readErr
}

func (r *Reader) readPages(ctx context.Context, pdfReader *ledongthuc.Reader, total int) ([]*document.Document, error) {
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
		if err := md.Set(MetadataPageIndex, page); err != nil {
			return nil, fmt.Errorf("pdf: page %d metadata: %w", page, err)
		}
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
	if r.sourceName != "" {
		if err := md.Set(MetadataSourceName, r.sourceName); err != nil {
			return nil, err
		}
	}
	return md, nil
}

// readAllText streams every page through [pageText] and concatenates
// the result. Using the per-page API instead of Reader.GetPlainText so
// a single bad page is skipped without aborting the whole document;
// page errors are joined and returned with any text that was decoded. ctx
// cancellation is honored between pages.
func (r *Reader) readAllText(ctx context.Context, pdfReader *ledongthuc.Reader, total int) (string, error) {
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
			failures = append(failures, err)
			continue
		}
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

// pageText extracts one page's plain text. The upstream parser panics
// on malformed page content (its panic-as-error style only recovers
// inside GetPlainText itself, not in Page / object resolution), so the
// recover here converts a bad page into an error the caller can skip.
func (*Reader) pageText(pdfReader *ledongthuc.Reader, pageIndex int) (text string, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			text, err = "", fmt.Errorf("page %d: malformed page: %v", pageIndex, rec)
		}
	}()
	page := pdfReader.Page(pageIndex)
	if page.V.IsNull() {
		return "", nil
	}
	text, err = page.GetPlainText(nil)
	if err != nil {
		return "", fmt.Errorf("page %d: %w", pageIndex, err)
	}
	return text, nil
}
