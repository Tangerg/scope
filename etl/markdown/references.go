package markdown

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/parser"
	"github.com/yuin/goldmark/v2/text"
)

const DefaultMaxExpandedBytes = 32 << 20

// ErrReferenceExpansionTooLarge means no partially expanded source is exposed.
var ErrReferenceExpansionTooLarge = errors.New("markdown: reference expansion exceeds byte limit")

var (
	destinationEscaper = strings.NewReplacer("&", "&amp;", "\\", "\\\\", "<", "\\<", ">", "\\>")
	titleEscaper       = strings.NewReplacer("&", "&amp;", "\\", "\\\\", "\"", "\\\"")
)

type referenceEdit struct {
	start       int
	end         int
	destination text.SingleLineValue
	title       text.MultiLineValue
}

func (r referenceEdit) writeTail(writer io.Writer, source []byte) error {
	if _, err := io.WriteString(writer, "(<"); err != nil {
		return err
	}
	if _, err := destinationEscaper.WriteString(writer, r.destination.Value(source)); err != nil {
		return err
	}
	if _, err := io.WriteString(writer, ">"); err != nil {
		return err
	}
	if !r.title.IsEmpty() {
		if _, err := io.WriteString(writer, " \""); err != nil {
			return err
		}
		if _, err := titleEscaper.WriteString(writer, r.title.Value(source)); err != nil {
			return err
		}
		if _, err := io.WriteString(writer, "\""); err != nil {
			return err
		}
	}
	_, err := io.WriteString(writer, ")")
	return err
}

type referenceResolver struct {
	parser   parser.Parser
	maxBytes int
}

func newReferenceResolver(markdownParser parser.Parser, maxBytes int) (referenceResolver, error) {
	if maxBytes < 0 {
		return referenceResolver{}, errors.New("markdown: expanded byte limit must not be negative")
	}
	if maxBytes == 0 {
		maxBytes = DefaultMaxExpandedBytes
	}
	return referenceResolver{parser: markdownParser, maxBytes: maxBytes}, nil
}

func (r referenceResolver) checkSize(size int) error {
	if size > r.maxBytes {
		return fmt.Errorf("%w: maximum is %d bytes", ErrReferenceExpansionTooLarge, r.maxBytes)
	}
	return nil
}

// Resolving reference tails before cutting keeps the source parser's first
// definition, case folding and escape rules authoritative. Each emitted link
// then owns all syntax needed to parse independently; its bytes count in the
// ordinary chunk budget. The counting pass shares the encoder and retains only
// source ranges, so repeated definitions cannot allocate unbounded tails.
func (r referenceResolver) resolve(ctx context.Context, source []byte) ([]byte, ast.Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := r.checkSize(len(source)); err != nil {
		return nil, nil, err
	}
	root := r.parser.Parse(source)
	var edits []referenceEdit
	err := ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if err := ctx.Err(); err != nil {
			return ast.WalkStop, err
		}
		var reference *ast.ReferenceLink
		var destination text.SingleLineValue
		var title text.MultiLineValue
		switch link := node.(type) {
		case *ast.Link:
			reference, destination, title = link.Reference, link.Destination, link.Title
		case *ast.Image:
			reference, destination, title = link.Reference, link.Destination, link.Title
		}
		if reference == nil {
			return ast.WalkContinue, nil
		}
		indices := reference.Value.Indices()
		if len(indices) == 0 {
			return ast.WalkStop, fmt.Errorf("markdown: reference has no source range")
		}
		end := indices[len(indices)-1].Stop + 1
		start := indices[0].Start - 1
		if reference.ReferenceLinkKind == ast.ReferenceLinkKindShortcut {
			start = end
		}
		if start < node.Pos() || end < start || end > len(source) {
			return ast.WalkStop, fmt.Errorf("markdown: invalid reference source range")
		}
		edits = append(edits, referenceEdit{start: start, end: end, destination: destination, title: title})
		return ast.WalkContinue, nil
	})
	if err != nil {
		return nil, nil, err
	}
	if len(edits) == 0 {
		return source, root, nil
	}
	slices.SortFunc(edits, func(left, right referenceEdit) int { return left.start - right.start })
	unchangedBytes := len(source)
	offset := 0
	for _, edit := range edits {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if edit.start < offset {
			return nil, nil, fmt.Errorf("markdown: overlapping reference tails")
		}
		unchangedBytes -= edit.end - edit.start
		offset = edit.end
	}
	allowance := referenceAllowance{remaining: r.maxBytes - unchangedBytes}
	for _, edit := range edits {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if err := edit.writeTail(&allowance, source); err != nil {
			return nil, nil, fmt.Errorf("markdown: expand reference: %w", err)
		}
	}
	var resolved bytes.Buffer
	resolved.Grow(r.maxBytes - allowance.remaining)
	offset = 0
	for _, edit := range edits {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		resolved.Write(source[offset:edit.start])
		if err := edit.writeTail(&resolved, source); err != nil {
			return nil, nil, err
		}
		offset = edit.end
	}
	resolved.Write(source[offset:])
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	source = resolved.Bytes()
	root = r.parser.Parse(source)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return source, root, nil
}

type referenceAllowance struct{ remaining int }

func (r *referenceAllowance) Write(data []byte) (int, error) { return r.consume(len(data)) }

func (r *referenceAllowance) WriteString(value string) (int, error) { return r.consume(len(value)) }

func (r *referenceAllowance) consume(size int) (int, error) {
	if size > r.remaining {
		return 0, ErrReferenceExpansionTooLarge
	}
	r.remaining -= size
	return size, nil
}

type inlineRange struct {
	start int
	end   int
}

// The token search may probe any rune boundary. Returning an empty rendering
// rejects boundaries inside links instead of emitting syntactically broken
// fragments that merely happen to fit the tokenizer.
func (r referenceResolver) inlineRanges(source string) []inlineRange {
	raw := []byte(source)
	var ranges []inlineRange
	_ = ast.Walk(r.parser.Parse(raw), func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var destination text.SingleLineValue
		var title text.MultiLineValue
		switch link := node.(type) {
		case *ast.Link:
			destination, title = link.Destination, link.Title
		case *ast.Image:
			destination, title = link.Destination, link.Title
		default:
			return ast.WalkContinue, nil
		}
		end := destination.Index().Stop
		if indices := title.Indices(); !title.IsEmpty() && len(indices) > 0 {
			end = indices[len(indices)-1].Stop + 1
		}
		if end > node.Pos() && end < len(source) {
			if closing := strings.IndexByte(source[end:], ')'); closing >= 0 {
				ranges = append(ranges, inlineRange{start: node.Pos(), end: end + closing + 1})
			}
		}
		return ast.WalkContinue, nil
	})
	return ranges
}

func (i inlineRange) containsBoundary(offset int) bool { return i.start < offset && offset < i.end }
