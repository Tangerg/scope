package parser

import (
	"bufio"
	"fmt"
	"io"
)

type ascii85PayloadReader struct {
	source *bufio.Reader
	done   bool
}

func newASCII85PayloadReader(source io.Reader) *ascii85PayloadReader {
	return &ascii85PayloadReader{source: bufio.NewReader(source)}
}

func (a *ascii85PayloadReader) Read(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	if a.done {
		return 0, io.EOF
	}
	count := 0
	for count < len(data) {
		next, err := a.source.ReadByte()
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return count, err
		}
		if isSpace(next) {
			continue
		}
		if next == '~' {
			end, readErr := a.source.ReadByte()
			if readErr != nil || end != '>' {
				return count, fmt.Errorf("malformed PDF: incomplete ASCII85 terminator")
			}
			a.done = true
			return count, io.EOF
		}
		if next != 'z' && (next < '!' || next > 'u') {
			return count, fmt.Errorf("malformed PDF: invalid ASCII85 character")
		}
		data[count] = next
		count++
	}
	return count, nil
}
