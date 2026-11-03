package parser

import (
	"errors"
	"io"
)

type filteredReader struct {
	io.Reader
	closers []io.Closer
}

func (f *filteredReader) Close() error {
	var failures []error
	for index := len(f.closers) - 1; index >= 0; index-- {
		if err := f.closers[index].Close(); err != nil {
			failures = append(failures, err)
		}
	}
	f.closers = nil
	return errors.Join(failures...)
}
