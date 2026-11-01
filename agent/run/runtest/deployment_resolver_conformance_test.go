package runtest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

// resolverTestDefinition supplies the Descriptor a Program freezes. The
// resolver contract never runs an Execution, so Start and Restore stay unused.
type resolverTestDefinition struct{ descriptor run.Descriptor }

func (r resolverTestDefinition) Descriptor() run.Descriptor { return r.descriptor }

func (resolverTestDefinition) ChildDeployments() []run.Program[run.Payload, run.Payload] {
	return nil
}

func (r resolverTestDefinition) Start(run.Payload) (run.Execution, error) {
	return nil, errors.New("agenttest: resolver fixture does not execute")
}

func (r resolverTestDefinition) Restore(context.Context, run.ExecutionState) (run.Execution, error) {
	return nil, errors.New("agenttest: resolver fixture does not execute")
}

func resolverTestDeployment(t *testing.T, name string) run.Program[run.Payload, run.Payload] {
	t.Helper()
	schema, err := jsonschema.For[definitionConformanceInput]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name: name, Description: "Exercise ProgramResolver conformance.",
		InputSchema: schema, OutputSchema: schema,
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition:           resolverTestDefinition{descriptor: descriptor},
		ImplementationDigest: run.ComputeDigest([]byte("agenttest.resolver.code")),
		ConfigurationDigest:  run.ComputeDigest([]byte("agenttest.resolver.config." + name)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

type exactResolver struct {
	bindings map[run.ProgramRef]run.Program[run.Payload, run.Payload]
}

func (e exactResolver) Resolve(reference run.ProgramRef) (run.Program[run.Payload, run.Payload], error) {
	deployment, found := e.bindings[reference]
	if !found {
		return run.Program[run.Payload, run.Payload]{}, fmt.Errorf("agenttest: no binding for %s", reference)
	}
	return deployment, nil
}

// nameFallbackResolver keys by name, which is exactly the mistake the contract
// forbids and the suite exists to catch.
type nameFallbackResolver struct {
	bindings map[string]run.Program[run.Payload, run.Payload]
}

func (n nameFallbackResolver) Resolve(reference run.ProgramRef) (run.Program[run.Payload, run.Payload], error) {
	deployment, found := n.bindings[reference.Name()]
	if !found {
		return run.Program[run.Payload, run.Payload]{}, fmt.Errorf("agenttest: no binding named %s", reference.Name())
	}
	return deployment, nil
}

type mismatchResolver struct {
	deployment run.Program[run.Payload, run.Payload]
}

func (m mismatchResolver) Resolve(run.ProgramRef) (run.Program[run.Payload, run.Payload], error) {
	return m.deployment, nil
}

type statefulResolver struct {
	bindings map[run.ProgramRef]run.Program[run.Payload, run.Payload]
	seen     map[run.ProgramRef]struct{}
}

func (s *statefulResolver) Resolve(reference run.ProgramRef) (run.Program[run.Payload, run.Payload], error) {
	if s.seen == nil {
		s.seen = make(map[run.ProgramRef]struct{})
	}
	s.seen[reference] = struct{}{}
	if len(s.seen) > 1 {
		return run.Program[run.Payload, run.Payload]{}, errors.New("agenttest: resolver retained caller state")
	}
	deployment, found := s.bindings[reference]
	if !found {
		return run.Program[run.Payload, run.Payload]{}, fmt.Errorf("agenttest: no binding for %s", reference)
	}
	return deployment, nil
}

type panickingResolver struct{}

func (panickingResolver) Resolve(run.ProgramRef) (run.Program[run.Payload, run.Payload], error) {
	panic("agenttest: resolver panicked")
}

func resolverConformanceFixture(t *testing.T) (first, second run.Program[run.Payload, run.Payload], exact exactResolver) {
	t.Helper()
	first = resolverTestDeployment(t, "agenttest.resolver.first")
	second = resolverTestDeployment(t, "agenttest.resolver.second")
	return first, second, exactResolver{bindings: map[run.ProgramRef]run.Program[run.Payload, run.Payload]{
		first.ProgramRef(): first, second.ProgramRef(): second,
	}}
}

func TestRunProgramResolverConformanceAcceptsAnExactResolver(t *testing.T) {
	first, second, exact := resolverConformanceFixture(t)
	unknown := resolverTestDeployment(t, "agenttest.resolver.unknown")
	RunProgramResolverConformance(t, ProgramResolverConformanceConfig{
		Resolver:     exact,
		Resolvable:   []run.Program[run.Payload, run.Payload]{first, second},
		Unresolvable: []run.ProgramRef{unknown.ProgramRef()},
	})
}

func TestProgramResolverConformanceDetectsContractViolations(t *testing.T) {
	first, second, _ := resolverConformanceFixture(t)
	for _, test := range []struct {
		name   string
		verify func(ProgramResolverConformanceConfig) error
		config ProgramResolverConformanceConfig
		want   string
	}{
		{
			"name fallback",
			verifyNameCollisionRejected,
			ProgramResolverConformanceConfig{
				Resolver: nameFallbackResolver{bindings: map[string]run.Program[run.Payload, run.Payload]{
					first.ProgramRef().Name(): first,
				}},
				Resolvable: []run.Program[run.Payload, run.Payload]{first},
			},
			"answered agenttest.resolver.first by name",
		},
		{
			"mismatched binding",
			verifyExactResolution,
			ProgramResolverConformanceConfig{
				Resolver:   mismatchResolver{deployment: second},
				Resolvable: []run.Program[run.Payload, run.Payload]{first},
			},
			"resolver answered",
		},
		{
			"retained caller state",
			verifyRepeatedResolution,
			ProgramResolverConformanceConfig{
				Resolver: &statefulResolver{bindings: map[run.ProgramRef]run.Program[run.Payload, run.Payload]{
					first.ProgramRef(): first, second.ProgramRef(): second,
				}},
				Resolvable: []run.Program[run.Payload, run.Payload]{first, second},
			},
			"retained caller state",
		},
		{
			"admitted unknown reference",
			verifyUnknownReferencesRejected,
			ProgramResolverConformanceConfig{
				Resolver:     mismatchResolver{deployment: first},
				Resolvable:   []run.Program[run.Payload, run.Payload]{first},
				Unresolvable: []run.ProgramRef{second.ProgramRef()},
			},
			"answered unknown reference",
		},
		{
			"panicked lookup",
			verifyExactResolution,
			ProgramResolverConformanceConfig{
				Resolver:   panickingResolver{},
				Resolvable: []run.Program[run.Payload, run.Payload]{first},
			},
			"panicked",
		},
		{
			"concurrent mismatch",
			verifyConcurrentResolution,
			ProgramResolverConformanceConfig{
				Resolver:   mismatchResolver{deployment: second},
				Resolvable: []run.Program[run.Payload, run.Payload]{first},
			},
			"resolver answered",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.verify(test.config)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("conformance error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestProgramResolverConformanceRejectsAnUnusableConfig(t *testing.T) {
	first, second, exact := resolverConformanceFixture(t)
	for _, test := range []struct {
		name   string
		config ProgramResolverConformanceConfig
		want   string
	}{
		{"nil resolver", ProgramResolverConformanceConfig{Resolvable: []run.Program[run.Payload, run.Payload]{first}}, "Resolver is nil"},
		{"no bindings", ProgramResolverConformanceConfig{Resolver: exact}, "at least one resolvable Deployment"},
		{
			"invalid binding",
			ProgramResolverConformanceConfig{Resolver: exact, Resolvable: []run.Program[run.Payload, run.Payload]{{}}},
			"Resolvable[0] is invalid",
		},
		{
			"duplicate binding",
			ProgramResolverConformanceConfig{Resolver: exact, Resolvable: []run.Program[run.Payload, run.Payload]{first, first}},
			"Resolvable[1] repeats",
		},
		{
			"invalid unresolvable",
			ProgramResolverConformanceConfig{
				Resolver: exact, Resolvable: []run.Program[run.Payload, run.Payload]{first},
				Unresolvable: []run.ProgramRef{{}},
			},
			"Unresolvable[0] is invalid",
		},
		{
			"contradictory unresolvable",
			ProgramResolverConformanceConfig{
				Resolver: exact, Resolvable: []run.Program[run.Payload, run.Payload]{first, second},
				Unresolvable: []run.ProgramRef{second.ProgramRef()},
			},
			"also listed as resolvable",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateProgramResolverConformanceConfig(test.config)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("config error = %v, want %q", err, test.want)
			}
		})
	}
}
