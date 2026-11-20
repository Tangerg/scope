package fs

import (
	"errors"
	"fmt"
	iofs "io/fs"

	toolcontract "github.com/Tangerg/scope/core/tool"
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
		if !errors.Is(cause, target) {
			continue
		}
		failure, err := toolcontract.NewFailure(toolcontract.FailureConfig{
			Kind: toolcontract.FailureKindFailed, Cause: cause, Output: toolcontract.NewTextOutput(fmt.Sprintf("%s: %s", operation, target)),
		})
		if err != nil {
			return errors.Join(cause, err)
		}
		return failure
	}
	return cause
}
