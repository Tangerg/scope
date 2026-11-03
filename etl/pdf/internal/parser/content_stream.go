package parser

import "io"

// Opening an array's streams lazily prevents all decompressors from retaining
// their dictionaries at once before any expanded bytes reach the budget.
type contentStream struct {
	source  Value
	index   int
	current io.ReadCloser
}

func (c *contentStream) Read(data []byte) (int, error) {
	count := 1
	if c.source.Kind() == Array {
		count = c.source.Len()
	}
	for c.index < count {
		c.source.r.budget.step(1)
		if c.current == nil {
			value := c.source
			if value.Kind() == Array {
				value = value.Index(c.index)
			}
			c.current = value.Reader()
		}
		n, err := c.current.Read(data)
		if err == io.EOF {
			closeErr := c.Close()
			c.index++
			if closeErr != nil {
				return n, closeErr
			}
			if n == 0 {
				continue
			}
			return n, nil
		}
		return n, err
	}
	return 0, io.EOF
}

func (c *contentStream) Close() error {
	if c.current == nil {
		return nil
	}
	err := c.current.Close()
	c.current = nil
	return err
}
