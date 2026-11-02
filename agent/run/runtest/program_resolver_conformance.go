package runtest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/agent/run"
)

type ProgramResolverConformanceConfig struct {
	Resolver run.ProgramResolver

	// Resolvable lists every Program the resolver must return for that
	// Program's own reference. At least one is required. The suite derives
	// its name-collision probes from these, so a resolver that falls back by
	// name is detected without the caller constructing the collision.
	Resolvable []run.Program[run.Payload, run.Payload]

	// Unresolvable lists references the resolver must reject. It may be empty;
	// the derived collision probes already exercise rejection.
	Unresolvable []run.ProgramRef
}

// RunProgramResolverConformance verifies the behavior a ProgramResolver
// promises but no compiler checks: exact reference matching, rejection of a
// reference that only shares a name, rejection of unknown references, and
// lookups that stay independent of their order, repetition, and concurrency.
//
// Boundedness, absence of remote input/output, and absence of Process
// re-entry cannot be observed from outside an implementation and remain the
// implementation's own tests. Run this suite under -race so the concurrency
// case can report a data race.
func RunProgramResolverConformance(t *testing.T, config ProgramResolverConformanceConfig) {
	t.Helper()
	if err := validateProgramResolverConformanceConfig(config); err != nil {
		t.Fatal(err)
	}

	for _, check := range []struct {
		name   string
		verify func(ProgramResolverConformanceConfig) error
	}{
		{"exact references resolve to their binding", verifyExactResolution},
		{"a name collision does not resolve", verifyNameCollisionRejected},
		{"unknown references are rejected", verifyUnknownReferencesRejected},
		{"repeated and interleaved lookups agree", verifyRepeatedResolution},
		{"concurrent lookups agree", verifyConcurrentResolution},
	} {
		t.Run(check.name, func(t *testing.T) {
			if err := check.verify(config); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func verifyExactResolution(config ProgramResolverConformanceConfig) error {
	for _, program := range config.Resolvable {
		reference := program.ProgramRef()
		resolved, err := callResolve(config.Resolver, reference)
		if err != nil {
			return fmt.Errorf("runtest: resolver rejected its own binding %s: %w", reference.Name(), err)
		}
		if err := requireSameBinding(program, resolved); err != nil {
			return err
		}
	}
	return nil
}

func verifyNameCollisionRejected(config ProgramResolverConformanceConfig) error {
	for _, program := range config.Resolvable {
		collision, err := nameCollisionReference(program)
		if err != nil {
			return err
		}
		resolved, resolveErr := callResolve(config.Resolver, collision)
		if resolveErr == nil {
			return fmt.Errorf(
				"runtest: resolver answered %s by name; it returned %s for different frozen configuration",
				collision.Name(), resolved.ProgramRef(),
			)
		}
	}
	return nil
}

func verifyUnknownReferencesRejected(config ProgramResolverConformanceConfig) error {
	for _, reference := range config.Unresolvable {
		resolved, err := callResolve(config.Resolver, reference)
		if err == nil {
			return fmt.Errorf(
				"runtest: resolver answered unknown reference %s with %s",
				reference.Name(), resolved.ProgramRef(),
			)
		}
	}
	return nil
}

func verifyRepeatedResolution(config ProgramResolverConformanceConfig) error {
	for round := range 2 {
		for _, program := range config.Resolvable {
			reference := program.ProgramRef()
			resolved, err := callResolve(config.Resolver, reference)
			if err != nil {
				return fmt.Errorf("runtest: round %d rejected %s: %w", round, reference.Name(), err)
			}
			if err := requireSameBinding(program, resolved); err != nil {
				return fmt.Errorf("runtest: round %d: %w", round, err)
			}
		}
	}
	return nil
}

func verifyConcurrentResolution(config ProgramResolverConformanceConfig) error {
	var group sync.WaitGroup
	failures := make(chan error, 2*len(config.Resolvable))
	for range 2 {
		for _, program := range config.Resolvable {
			group.Add(1)
			go func() {
				defer group.Done()
				reference := program.ProgramRef()
				resolved, err := callResolve(config.Resolver, reference)
				if err != nil {
					failures <- fmt.Errorf("runtest: concurrent lookup rejected %s: %w", reference.Name(), err)
					return
				}
				if err := requireSameBinding(program, resolved); err != nil {
					failures <- err
				}
			}()
		}
	}
	group.Wait()
	close(failures)
	var collected []error
	for err := range failures {
		collected = append(collected, err)
	}
	return errors.Join(collected...)
}

func validateProgramResolverConformanceConfig(config ProgramResolverConformanceConfig) error {
	if lo.IsNil(config.Resolver) {
		return errors.New("runtest: ProgramResolver conformance Resolver is nil")
	}
	if len(config.Resolvable) == 0 {
		return errors.New("runtest: ProgramResolver conformance requires at least one resolvable Program")
	}
	seen := make(map[run.ProgramRef]struct{}, len(config.Resolvable))
	for index, program := range config.Resolvable {
		if !program.Valid() {
			return fmt.Errorf("runtest: ProgramResolver conformance Resolvable[%d] is invalid", index)
		}
		reference := program.ProgramRef()
		if _, duplicate := seen[reference]; duplicate {
			return fmt.Errorf("runtest: ProgramResolver conformance Resolvable[%d] repeats %s", index, reference)
		}
		seen[reference] = struct{}{}
	}
	for index, reference := range config.Unresolvable {
		if !reference.Valid() {
			return fmt.Errorf("runtest: ProgramResolver conformance Unresolvable[%d] is invalid", index)
		}
		if _, resolvable := seen[reference]; resolvable {
			return fmt.Errorf(
				"runtest: ProgramResolver conformance Unresolvable[%d] is also listed as resolvable", index,
			)
		}
	}
	return nil
}

// Only an exact-reference resolver rejects a binding with the same name,
// contract and implementation but different frozen configuration.
func nameCollisionReference(program run.Program[run.Payload, run.Payload]) (run.ProgramRef, error) {
	reference := program.ProgramRef()
	ctx := context.Background()
	runtime, err := run.New(ctx, run.Config{Store: run.NewMemoryStore(), Build: reference.Build()})
	if err != nil {
		return run.ProgramRef{}, err
	}
	probe, bindErr := run.Bind[run.Payload, run.Payload](runtime.Compiler(), run.BindingConfig{
		Definition:    program.Definition(),
		Revision:      "runtest.resolver.collision",
		Configuration: reference.String(),
	})
	if err := errors.Join(bindErr, runtime.Close(ctx)); err != nil {
		return run.ProgramRef{}, fmt.Errorf("runtest: derive a name collision for %s: %w", reference.Name(), err)
	}
	collision := probe.ProgramRef()
	if collision == reference {
		return run.ProgramRef{}, fmt.Errorf("runtest: name collision probe for %s reproduced the original reference", reference.Name())
	}
	return collision, nil
}

func requireSameBinding(want, got run.Program[run.Payload, run.Payload]) error {
	reference := want.ProgramRef()
	if !got.Valid() {
		return fmt.Errorf("runtest: resolver returned an invalid Program for %s", reference.Name())
	}
	if got.ProgramRef() != reference {
		return fmt.Errorf(
			"runtest: resolver answered %s with %s", reference, got.ProgramRef(),
		)
	}
	return nil
}

func callResolve(
	resolver run.ProgramResolver,
	reference run.ProgramRef,
) (program run.Program[run.Payload, run.Payload], err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			program = run.Program[run.Payload, run.Payload]{}
			err = &run.CallbackPanicError{Operation: "ProgramResolver.Resolve", Value: recovered}
		}
	}()
	return resolver.Resolve(reference)
}
