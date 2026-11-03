package fs

import (
	"errors"
	"fmt"
	iofs "io/fs"

	"github.com/Tangerg/scope/tools/internal/toolresult"
)

// correctableLookupErrors are the read, glob, and grep outcomes a model can
// fix by changing its arguments. The model sees the matched sentinel's fixed
// text, never the wrapped error, which can name host paths. Any other error
// stays a backend fault that the Binding settles without detail.
var correctableLookupErrors = []error{
	iofs.ErrNotExist, iofs.ErrPermission, ErrEmptyPath, ErrInvalidInput, ErrPathOutsideRoot,
	ErrEmptyPattern, ErrBinaryFile, ErrFileTooLarge, ErrLineTooLarge,
}

func lookupError(operation string, cause error) error {
	cause = fmt.Errorf("%s: %w", operation, cause)
	for _, target := range correctableLookupErrors {
		if errors.Is(cause, target) {
			return toolresult.Correctable(fmt.Sprintf("error: %s: %s", operation, target), cause)
		}
	}
	return cause
}
