package tool

import (
	"fmt"
)

// InputValidatingTool supplies the input admission rules that JSON Schema
// cannot express, such as Go numeric syntax or a custom JSON decoder's domain
// constraints. Bind captures the declaration directly from the executable before composing middleware.
type InputValidatingTool interface {
	// InputValidator returns an independent, immutable validator. Neither the
	// returned function nor anything it captures may retain an executable Tool
	// or execution backend. It must be deterministic, bounded, side-effect-free,
	// safe for concurrent use, and must not mutate or retain its argument.
	// Return an InputRejection only for model-correctable input. Its Message is
	// public; ordinary errors and panics are internal validator defects.
	// Nil means the schema is the complete input admission contract.
	InputValidator() func([]byte) error
}

func inputValidator(executable Tool) (validator func([]byte) error, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			validator = nil
			err = &InputValidationError{cause: &InputValidationPanicError{Value: recovered}}
		}
	}()
	capability, found := executable.(InputValidatingTool)
	if !found {
		return nil, nil
	}
	return capability.InputValidator(), nil
}

// InputValidationPanicError identifies a contained panic while declaring or running
// input validation. It is retained only behind InputValidationError.Cause;
// this diagnostic cannot turn a panic into public input rejection or execution evidence.
// Value retains the recovered value; an error value remains available through Unwrap.
type InputValidationPanicError struct{ Value any }

func (i *InputValidationPanicError) Error() string {
	return fmt.Sprintf("tool: input validation panicked: %v", i.Value)
}
func (i *InputValidationPanicError) Unwrap() error {
	cause, _ := i.Value.(error)
	return cause
}
