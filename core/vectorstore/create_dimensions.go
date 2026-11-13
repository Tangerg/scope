package vectorstore

import "fmt"

// CreateDimensions is a store constructor's provisioning request for a table
// whose native vector column owns its width. Zero opens an existing table. A
// positive width creates a missing table with that width and requires an
// existing table to declare the same width.
type CreateDimensions int

func (c CreateDimensions) Validate() error {
	if c < 0 {
		return fmt.Errorf("%w: create dimensions must not be negative", ErrIncompatibleSpace)
	}
	return nil
}

// Width reports the width a missing table is created with.
func (c CreateDimensions) Width() (int, bool) { return int(c), c > 0 }

// Admit checks the width an opened table declares against the request.
func (c CreateDimensions) Admit(declared int) error {
	if c > 0 && declared != int(c) {
		return fmt.Errorf("%w: table declares %d dimensions, not %d", ErrIncompatibleSpace, declared, c)
	}
	return nil
}
