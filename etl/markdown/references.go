package markdown

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/parser"
	"github.com/yuin/goldmark/v2/text"
)

var (
	destinationEscaper = strings.NewReplacer("&", "&amp;", "\\", "\\\\", "<", "\\<", ">", "\\>")
	titleEscaper       = strings.NewReplacer("&", "&amp;", "\\", "\\\\", "\"", "\\\"")
)

type referenceEdit struct {
	start int
	end   int
	tail  string
}

// Resolving reference tails before cutting keeps the source parser's first
// definition, case folding and escape rules authoritative. Each emitted link
// then owns all syntax needed to parse independently; its bytes count in the
// ordinary chunk budget. Tail-only edits also preserve nested image labels.
func resolveReferences(ctx context.Context, markdownParser parser.Parser, source []byte) ([]byte, ast.Node, error) {
	root := markdownParser.Parse(source)
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
		tail := "(<" + destinationEscaper.Replace(destination.Value(source)) + ">"
		if !title.IsEmpty() {
			tail += " \"" + titleEscaper.Replace(title.Value(source)) + "\""
		}
		edits = append(edits, referenceEdit{start: start, end: end, tail: tail + ")"})
		return ast.WalkContinue, nil
	})
	if err != nil {
		return nil, nil, err
	}
	if len(edits) == 0 {
		return source, root, nil
	}
	slices.SortFunc(edits, func(left, right referenceEdit) int { return left.start - right.start })
	var resolved strings.Builder
	offset := 0
	for _, edit := range edits {
		if edit.start < offset {
			return nil, nil, fmt.Errorf("markdown: overlapping reference tails")
		}
		resolved.Write(source[offset:edit.start])
		resolved.WriteString(edit.tail)
		offset = edit.end
	}
	resolved.Write(source[offset:])
	source = []byte(resolved.String())
	return source, markdownParser.Parse(source), nil
}

type inlineRange struct {
	start int
	end   int
}

// The token search may probe any rune boundary. Returning an empty rendering
// rejects boundaries inside links instead of emitting syntactically broken
// fragments that merely happen to fit the tokenizer.
func inlineRanges(markdownParser parser.Parser, source string) []inlineRange {
	raw := []byte(source)
	var ranges []inlineRange
	_ = ast.Walk(markdownParser.Parse(raw), func(node ast.Node, entering bool) (ast.WalkStatus, error) {
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
