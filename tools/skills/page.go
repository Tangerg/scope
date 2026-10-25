package skills

import (
	"crypto/sha256"
	"encoding/hex"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"unicode/utf8"

	skillsrc "github.com/Tangerg/scope/skills"
)

var (
	ErrInvalidCursor = errors.New("skills: cursor does not match the requested content")
	ErrOutputLimit   = errors.New("skills: output limit cannot fit one page item and its cursor")
)

// Cursor identifies a position in one exact content snapshot. It is data, not
// authority: every page reopens the same source and checks its current content.
type Cursor struct {
	Offset   int    `json:"offset" jsonschema:"minimum=1"`
	Snapshot string `json:"snapshot" jsonschema:"minLength=64,maxLength=64"`
}

func (c *Cursor) validate() error {
	if c == nil {
		return nil
	}
	if c.Offset <= 0 || len(c.Snapshot) != sha256.Size*2 {
		return ErrInvalidCursor
	}
	if _, err := hex.DecodeString(c.Snapshot); err != nil {
		return ErrInvalidCursor
	}
	return nil
}

func (c *Cursor) start(snapshot string, length int) (int, error) {
	if err := c.validate(); err != nil {
		return 0, err
	}
	if c == nil {
		return 0, nil
	}
	if c.Snapshot != snapshot || c.Offset >= length {
		return 0, ErrInvalidCursor
	}
	return c.Offset, nil
}

// ListPage never splits a summary. Absence of NextCursor means the list is complete.
type ListPage struct {
	Skills     []skillsrc.Summary `json:"skills"`
	NextCursor *Cursor            `json:"next_cursor,omitzero"`
}

// TextPage preserves UTF-8 and every byte of the source. Concatenate Content
// across pages until NextCursor is absent before treating instructions as complete.
type TextPage struct {
	Content    string  `json:"content"`
	NextCursor *Cursor `json:"next_cursor,omitzero"`
}

func contentSnapshot(operation, name, path string, content []byte) string {
	digest := sha256.New()
	_, _ = fmt.Fprintf(digest, "%s\x00%s\x00%s\x00", operation, name, path)
	_, _ = digest.Write(content)
	return hex.EncodeToString(digest.Sum(nil))
}

// The final page has no cursor overhead, so test it before finding the largest
// prefix. A nonfinal page must advance; an empty page could otherwise loop forever.
func boundedPage[T any](remaining int, maxBytes int64, makePage func(int) T) (T, error) {
	complete := makePage(remaining)
	encoded, err := jsonv2.Marshal(complete)
	if err != nil {
		return complete, err
	}
	if int64(len(encoded)) <= maxBytes {
		return complete, nil
	}
	var best T
	found := false
	for low, high := 1, remaining-1; low <= high; {
		middle := low + (high-low)/2
		page := makePage(middle)
		encoded, err := jsonv2.Marshal(page)
		if err != nil {
			return best, err
		}
		if int64(len(encoded)) <= maxBytes {
			best, found = page, true
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	if !found {
		return best, ErrOutputLimit
	}
	return best, nil
}

func textPage(operation, name, path, content string, cursor *Cursor, maxBytes int64) (TextPage, error) {
	if !utf8.ValidString(content) {
		return TextPage{}, fmt.Errorf("%w: content must be valid UTF-8", skillsrc.ErrInvalidSkill)
	}
	snapshot := contentSnapshot(operation, name, path, []byte(content))
	start, err := cursor.start(snapshot, len(content))
	if err != nil {
		return TextPage{}, err
	}
	if start < len(content) && !utf8.RuneStart(content[start]) {
		return TextPage{}, ErrInvalidCursor
	}
	page, err := boundedPage(len(content)-start, maxBytes, func(count int) TextPage {
		end := start + count
		for end > start && end < len(content) && !utf8.RuneStart(content[end]) {
			end--
		}
		page := TextPage{Content: content[start:end]}
		if end < len(content) {
			page.NextCursor = &Cursor{Offset: end, Snapshot: snapshot}
		}
		return page
	})
	if err == nil && page.NextCursor != nil && page.NextCursor.Offset <= start {
		return TextPage{}, ErrOutputLimit
	}
	return page, err
}
