package run

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/Tangerg/scope/core/jsonschema"

	"github.com/samber/lo"
)

var ErrInvalidProgram = errors.New("run: invalid program")

// Bind hashes the declaration after freezing its canonical JSON. Opaque
// collaborators must be identified by Revision in their declaring configuration.
type BindingConfig struct {
	Definition    Definition
	Dispatcher    Dispatcher
	Revision      string
	Configuration any
}

// Program is an immutable binding of one Definition, its optional external
// Dispatcher, and an exact value reference used by Process logs.
type Program[I, O any] struct {
	_          [0]*func(I) O
	reference  ProgramRef
	descriptor Descriptor
	definition Definition
	dispatcher Dispatcher
	// children are the canonical static child bindings, ordered by
	// reference digest. Their references make up reference's bindings digest.
	children []Program[Payload, Payload]
}

func Bind[I, O any](compiler *Compiler, config BindingConfig) (Program[I, O], error) {
	if compiler == nil || compiler.build == "" {
		return Program[I, O]{}, ErrInvalidProgram
	}
	if lo.IsNil(config.Definition) {
		return Program[I, O]{}, fmt.Errorf("%w: definition is required", ErrInvalidProgram)
	}
	if config.Dispatcher != nil && lo.IsNil(config.Dispatcher) {
		return Program[I, O]{}, fmt.Errorf("%w: dispatcher is typed nil", ErrInvalidProgram)
	}
	descriptor, err := definitionDescriptor(config.Definition)
	if err != nil {
		return Program[I, O]{}, err
	}
	if operationErr := matchesPort[I](descriptor.InputSchema()); operationErr != nil {
		return Program[I, O]{}, fmt.Errorf("%w: input: %w", ErrInvalidProgram, operationErr)
	}
	if operationErr := matchesPort[O](descriptor.OutputSchema()); operationErr != nil {
		return Program[I, O]{}, fmt.Errorf("%w: output: %w", ErrInvalidProgram, operationErr)
	}
	children, bindings, err := definitionBindings(config.Definition)
	if err != nil {
		return Program[I, O]{}, err
	}
	for _, child := range children {
		if child.ProgramRef().Build() != compiler.build {
			return Program[I, O]{}, fmt.Errorf("%w: child build differs from compiler build", ErrInvalidProgram)
		}
	}
	configuration, err := EncodePayload(config.Configuration)
	if err != nil {
		return Program[I, O]{}, fmt.Errorf("%w: configuration: %w", ErrInvalidProgram, err)
	}
	reference, err := newProgramRef(programIdentityWire{
		Name: descriptor.Name(), Build: compiler.build, Contract: descriptor.Digest(),
		Configuration: ComputeDigest(configuration.data), Revision: config.Revision, Children: bindings,
	})
	if err != nil {
		return Program[I, O]{}, fmt.Errorf("%w: %w", ErrInvalidProgram, err)
	}
	return Program[I, O]{
		reference:  reference,
		descriptor: descriptor,
		definition: config.Definition,
		dispatcher: config.Dispatcher,
		children:   children,
	}, nil
}

// Erase preserves the same frozen binding for heterogeneous child graphs.
func (p Program[I, O]) Erase() Program[Payload, Payload] {
	return Program[Payload, Payload]{reference: p.reference, descriptor: p.descriptor, definition: p.definition, dispatcher: p.dispatcher, children: p.children}
}

func matchesPort[T any](schema jsonschema.Schema) error {
	if reflect.TypeFor[T]() == reflect.TypeFor[Payload]() {
		return nil
	}
	actual, err := jsonschema.For[T]()
	if err != nil {
		return err
	}
	if !actual.Equal(schema) {
		return fmt.Errorf("type %v does not match the declared schema", reflect.TypeFor[T]())
	}
	return nil
}

func (p Program[I, O]) ProgramRef() ProgramRef { return p.reference }

func (p Program[I, O]) Descriptor() Descriptor { return p.descriptor }

func (p Program[I, O]) Definition() Definition { return p.definition }

func (p Program[I, O]) boundChild(reference ProgramRef) (Program[Payload, Payload], bool) {
	index, found := slices.BinarySearchFunc(p.children, reference, compareBoundChild)
	if !found {
		return Program[Payload, Payload]{}, false
	}
	return p.children[index], true
}

// Valid checks the frozen binding without invoking user code. The Runtime checks
// the live Definition contract at startup and restoration boundaries.
func (p Program[I, O]) Valid() bool {
	return p.reference.Valid() && p.descriptor.Valid() &&
		!lo.IsNil(p.definition) && (p.dispatcher == nil || !lo.IsNil(p.dispatcher))
}

func (p Program[I, O]) validateDefinition() error {
	if !p.Valid() {
		return ErrInvalidProgram
	}
	descriptor, err := definitionDescriptor(p.definition)
	if err != nil {
		return err
	}
	if descriptor.Digest() != p.descriptor.Digest() {
		return fmt.Errorf("%w: definition Descriptor differs from its frozen contract", ErrInvalidProgram)
	}
	_, bindings, err := definitionBindings(p.definition)
	if err != nil {
		return err
	}
	if bindings != p.reference.children {
		return fmt.Errorf("%w: definition child bindings differ from its frozen identity", ErrInvalidProgram)
	}
	return nil
}

func definitionBindings(definition Definition) ([]Program[Payload, Payload], Digest, error) {
	reported, err := invokeCallback("Definition.ChildPrograms", func() ([]Program[Payload, Payload], error) {
		return definition.ChildPrograms(), nil
	})
	if err != nil {
		return nil, Digest{}, fmt.Errorf("%w: %w", ErrInvalidProgram, err)
	}
	children := slices.Clone(reported)
	for _, child := range children {
		if !child.Valid() {
			return nil, Digest{}, fmt.Errorf("%w: child binding is invalid", ErrInvalidProgram)
		}
	}
	slices.SortFunc(children, func(left, right Program[Payload, Payload]) int {
		return compareBoundChild(left, right.reference)
	})
	children = slices.CompactFunc(children, func(left, right Program[Payload, Payload]) bool { return left.reference == right.reference })
	references := make([]ProgramRef, len(children))
	for index, child := range children {
		references[index] = child.reference
	}
	bindings, err := childBindingsDigest(references)
	if err != nil {
		return nil, Digest{}, fmt.Errorf("%w: bindings digest: %w", ErrInvalidProgram, err)
	}
	return children, bindings, nil
}

// compareBoundChild orders static bindings by reference digest, so equal
// binding sets always produce the same bindings digest.
func compareBoundChild(child Program[Payload, Payload], reference ProgramRef) int {
	return strings.Compare(child.reference.digest.String(), reference.digest.String())
}

func definitionDescriptor(definition Definition) (Descriptor, error) {
	descriptor, err := invokeCallback("Definition.Descriptor", func() (Descriptor, error) {
		return definition.Descriptor(), nil
	})
	if err != nil {
		return Descriptor{}, fmt.Errorf("%w: %w", ErrInvalidProgram, err)
	}
	return descriptor, nil
}
