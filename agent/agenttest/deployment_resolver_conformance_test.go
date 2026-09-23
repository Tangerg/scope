package agenttest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

// resolverTestDefinition supplies the Descriptor a Deployment freezes. The
// resolver contract never runs an Execution, so Start and Restore stay unused.
type resolverTestDefinition struct{ descriptor agent.Descriptor }

func (r resolverTestDefinition) Descriptor() agent.Descriptor { return r.descriptor }

func (r resolverTestDefinition) Start(agent.Payload) (agent.Execution, error) {
	return nil, errors.New("agenttest: resolver fixture does not execute")
}

func (r resolverTestDefinition) Restore(context.Context, agent.ExecutionState) (agent.Execution, error) {
	return nil, errors.New("agenttest: resolver fixture does not execute")
}

func resolverTestDeployment(t *testing.T, name string) agent.Deployment {
	t.Helper()
	schema, err := agent.SchemaFor[definitionConformanceInput]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: name, Description: "Exercise DeploymentResolver conformance.",
		InputSchema: schema, OutputSchema: schema,
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           resolverTestDefinition{descriptor: descriptor},
		ImplementationDigest: agent.ComputeDigest([]byte("agenttest.resolver.code")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("agenttest.resolver.config." + name)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

type exactResolver struct {
	bindings map[agent.DeploymentRef]agent.Deployment
}

func (e exactResolver) Resolve(reference agent.DeploymentRef) (agent.Deployment, error) {
	deployment, found := e.bindings[reference]
	if !found {
		return agent.Deployment{}, fmt.Errorf("agenttest: no binding for %s", reference)
	}
	return deployment, nil
}

// nameFallbackResolver keys by name, which is exactly the mistake the contract
// forbids and the suite exists to catch.
type nameFallbackResolver struct{ bindings map[string]agent.Deployment }

func (n nameFallbackResolver) Resolve(reference agent.DeploymentRef) (agent.Deployment, error) {
	deployment, found := n.bindings[reference.Name()]
	if !found {
		return agent.Deployment{}, fmt.Errorf("agenttest: no binding named %s", reference.Name())
	}
	return deployment, nil
}

// mismatchResolver answers every reference with one fixed binding.
type mismatchResolver struct{ deployment agent.Deployment }

func (m mismatchResolver) Resolve(agent.DeploymentRef) (agent.Deployment, error) {
	return m.deployment, nil
}

// statefulResolver answers only until a second reference passes through it.
type statefulResolver struct {
	bindings map[agent.DeploymentRef]agent.Deployment
	seen     map[agent.DeploymentRef]struct{}
}

func (s *statefulResolver) Resolve(reference agent.DeploymentRef) (agent.Deployment, error) {
	if s.seen == nil {
		s.seen = make(map[agent.DeploymentRef]struct{})
	}
	s.seen[reference] = struct{}{}
	if len(s.seen) > 1 {
		return agent.Deployment{}, errors.New("agenttest: resolver retained caller state")
	}
	deployment, found := s.bindings[reference]
	if !found {
		return agent.Deployment{}, fmt.Errorf("agenttest: no binding for %s", reference)
	}
	return deployment, nil
}

type panickingResolver struct{}

func (panickingResolver) Resolve(agent.DeploymentRef) (agent.Deployment, error) {
	panic("agenttest: resolver panicked")
}

func resolverConformanceFixture(t *testing.T) (first, second agent.Deployment, exact exactResolver) {
	t.Helper()
	first = resolverTestDeployment(t, "agenttest.resolver.first")
	second = resolverTestDeployment(t, "agenttest.resolver.second")
	return first, second, exactResolver{bindings: map[agent.DeploymentRef]agent.Deployment{
		first.DeploymentRef(): first, second.DeploymentRef(): second,
	}}
}

func TestRunDeploymentResolverConformanceAcceptsAnExactResolver(t *testing.T) {
	first, second, exact := resolverConformanceFixture(t)
	unknown := resolverTestDeployment(t, "agenttest.resolver.unknown")
	RunDeploymentResolverConformance(t, DeploymentResolverConformanceConfig{
		Resolver:     exact,
		Resolvable:   []agent.Deployment{first, second},
		Unresolvable: []agent.DeploymentRef{unknown.DeploymentRef()},
	})
}

func TestDeploymentResolverConformanceDetectsContractViolations(t *testing.T) {
	first, second, _ := resolverConformanceFixture(t)
	for _, test := range []struct {
		name   string
		verify func(DeploymentResolverConformanceConfig) error
		config DeploymentResolverConformanceConfig
		want   string
	}{
		{
			"name fallback",
			verifyNameCollisionRejected,
			DeploymentResolverConformanceConfig{
				Resolver: nameFallbackResolver{bindings: map[string]agent.Deployment{
					first.DeploymentRef().Name(): first,
				}},
				Resolvable: []agent.Deployment{first},
			},
			"answered agenttest.resolver.first by name",
		},
		{
			"mismatched binding",
			verifyExactResolution,
			DeploymentResolverConformanceConfig{
				Resolver:   mismatchResolver{deployment: second},
				Resolvable: []agent.Deployment{first},
			},
			"resolver answered",
		},
		{
			"retained caller state",
			verifyRepeatedResolution,
			DeploymentResolverConformanceConfig{
				Resolver: &statefulResolver{bindings: map[agent.DeploymentRef]agent.Deployment{
					first.DeploymentRef(): first, second.DeploymentRef(): second,
				}},
				Resolvable: []agent.Deployment{first, second},
			},
			"retained caller state",
		},
		{
			"admitted unknown reference",
			verifyUnknownReferencesRejected,
			DeploymentResolverConformanceConfig{
				Resolver:     mismatchResolver{deployment: first},
				Resolvable:   []agent.Deployment{first},
				Unresolvable: []agent.DeploymentRef{second.DeploymentRef()},
			},
			"answered unknown reference",
		},
		{
			"panicked lookup",
			verifyExactResolution,
			DeploymentResolverConformanceConfig{
				Resolver:   panickingResolver{},
				Resolvable: []agent.Deployment{first},
			},
			"panicked",
		},
		{
			"concurrent mismatch",
			verifyConcurrentResolution,
			DeploymentResolverConformanceConfig{
				Resolver:   mismatchResolver{deployment: second},
				Resolvable: []agent.Deployment{first},
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

func TestDeploymentResolverConformanceRejectsAnUnusableConfig(t *testing.T) {
	first, second, exact := resolverConformanceFixture(t)
	for _, test := range []struct {
		name   string
		config DeploymentResolverConformanceConfig
		want   string
	}{
		{"nil resolver", DeploymentResolverConformanceConfig{Resolvable: []agent.Deployment{first}}, "Resolver is nil"},
		{"no bindings", DeploymentResolverConformanceConfig{Resolver: exact}, "at least one resolvable Deployment"},
		{
			"invalid binding",
			DeploymentResolverConformanceConfig{Resolver: exact, Resolvable: []agent.Deployment{{}}},
			"Resolvable[0] is invalid",
		},
		{
			"duplicate binding",
			DeploymentResolverConformanceConfig{Resolver: exact, Resolvable: []agent.Deployment{first, first}},
			"Resolvable[1] repeats",
		},
		{
			"invalid unresolvable",
			DeploymentResolverConformanceConfig{
				Resolver: exact, Resolvable: []agent.Deployment{first},
				Unresolvable: []agent.DeploymentRef{{}},
			},
			"Unresolvable[0] is invalid",
		},
		{
			"contradictory unresolvable",
			DeploymentResolverConformanceConfig{
				Resolver: exact, Resolvable: []agent.Deployment{first, second},
				Unresolvable: []agent.DeploymentRef{second.DeploymentRef()},
			},
			"also listed as resolvable",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateDeploymentResolverConformanceConfig(test.config)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("config error = %v, want %q", err, test.want)
			}
		})
	}
}
