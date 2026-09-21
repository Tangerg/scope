package tool

import (
	"fmt"
)

// InputValidatingTool supplies the input admission rules that JSON Schema
// cannot express, such as Go numeric syntax or a custom JSON decoder's domain
// constraints. Bind freezes the declaration through the wrapping chain.
type InputValidatingTool interface {
	// InputValidator returns an independent, immutable validator. Neither the
	// returned function nor anything it captures may retain an executable Tool
	// or execution backend. It must be deterministic, bounded, side-effect-free,
	// safe for concurrent use, and must not mutate or retain its argument.
	// Nil means the schema is the complete input admission contract.
	InputValidator() func([]byte) error
}

func inputValidator(executable Tool) (validator func([]byte) error, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			validator = nil
			err = fmt.Errorf("input validation declaration: %w", &InputValidationPanicError{Value: recovered})
		}
	}()
	capability, found, err := Capability[InputValidatingTool](executable)
	if err != nil || !found {
		return nil, err
	}
	return capability.InputValidator(), nil
}

// InputValidationPanicError identifies a contained panic while declaring or running
// input validation. Prepare still rejects the invocation with ErrInvalidInvocation;
// this diagnostic distinguishes a validator defect from invalid caller input.
// Value retains the recovered value; an error value remains available through Unwrap.
type InputValidationPanicError struct{ Value any }

func (i *InputValidationPanicError) Error() string {
	return fmt.Sprintf("tool: input validation panicked: %v", i.Value)
}
func (i *InputValidationPanicError) Unwrap() error {
	cause, _ := i.Value.(error)
	return cause
}
