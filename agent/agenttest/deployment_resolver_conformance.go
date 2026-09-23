package agenttest

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/samber/lo"

	agent "github.com/Tangerg/scope/agent"
)

// DeploymentResolverConformanceConfig describes one resolver and the exact
// bindings it must answer.
type DeploymentResolverConformanceConfig struct {
	// Resolver is the implementation under test.
	Resolver agent.DeploymentResolver

	// Resolvable lists every Deployment the resolver must return for that
	// Deployment's own reference. At least one is required. The suite derives
	// its name-collision probes from these, so a resolver that falls back by
	// name is detected without the caller constructing the collision.
	Resolvable []agent.Deployment

	// Unresolvable lists references the resolver must reject. It may be empty;
	// the derived collision probes already exercise rejection.
	Unresolvable []agent.DeploymentRef
}

// RunDeploymentResolverConformance verifies the behavior a DeploymentResolver
// promises but no compiler checks: exact reference matching, rejection of a
// reference that only shares a name, rejection of unknown references, and
// lookups that stay independent of their order, repetition, and concurrency.
//
// Boundedness, absence of remote input/output, and absence of Process
// re-entry cannot be observed from outside an implementation and remain the
// implementation's own tests. Run this suite under -race so the concurrency
// case can report a data race.
func RunDeploymentResolverConformance(t *testing.T, config DeploymentResolverConformanceConfig) {
	t.Helper()
	if err := validateDeploymentResolverConformanceConfig(config); err != nil {
		t.Fatal(err)
	}

	for _, check := range []struct {
		name   string
		verify func(DeploymentResolverConformanceConfig) error
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

func verifyExactResolution(config DeploymentResolverConformanceConfig) error {
	for _, deployment := range config.Resolvable {
		reference := deployment.DeploymentRef()
		resolved, err := callResolve(config.Resolver, reference)
		if err != nil {
			return fmt.Errorf("agenttest: resolver rejected its own binding %s: %w", reference.Name(), err)
		}
		if err := requireSameBinding(reference, deployment, resolved); err != nil {
			return err
		}
	}
	return nil
}

func verifyNameCollisionRejected(config DeploymentResolverConformanceConfig) error {
	for _, deployment := range config.Resolvable {
		collision, err := nameCollisionReference(deployment)
		if err != nil {
			return err
		}
		resolved, resolveErr := callResolve(config.Resolver, collision)
		if resolveErr == nil {
			return fmt.Errorf(
				"agenttest: resolver answered %s by name; it returned %s for different frozen configuration",
				collision.Name(), resolved.DeploymentRef(),
			)
		}
	}
	return nil
}

func verifyUnknownReferencesRejected(config DeploymentResolverConformanceConfig) error {
	for _, reference := range config.Unresolvable {
		resolved, err := callResolve(config.Resolver, reference)
		if err == nil {
			return fmt.Errorf(
				"agenttest: resolver answered unknown reference %s with %s",
				reference.Name(), resolved.DeploymentRef(),
			)
		}
	}
	return nil
}

// verifyRepeatedResolution walks every reference twice: a resolver retaining
// caller state answers differently once another reference has passed through.
func verifyRepeatedResolution(config DeploymentResolverConformanceConfig) error {
	for round := range 2 {
		for _, deployment := range config.Resolvable {
			reference := deployment.DeploymentRef()
			resolved, err := callResolve(config.Resolver, reference)
			if err != nil {
				return fmt.Errorf("agenttest: round %d rejected %s: %w", round, reference.Name(), err)
			}
			if err := requireSameBinding(reference, deployment, resolved); err != nil {
				return fmt.Errorf("agenttest: round %d: %w", round, err)
			}
		}
	}
	return nil
}

func verifyConcurrentResolution(config DeploymentResolverConformanceConfig) error {
	var group sync.WaitGroup
	failures := make(chan error, 2*len(config.Resolvable))
	for range 2 {
		for _, deployment := range config.Resolvable {
			group.Add(1)
			go func() {
				defer group.Done()
				reference := deployment.DeploymentRef()
				resolved, err := callResolve(config.Resolver, reference)
				if err != nil {
					failures <- fmt.Errorf("agenttest: concurrent lookup rejected %s: %w", reference.Name(), err)
					return
				}
				if err := requireSameBinding(reference, deployment, resolved); err != nil {
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

func validateDeploymentResolverConformanceConfig(config DeploymentResolverConformanceConfig) error {
	if lo.IsNil(config.Resolver) {
		return errors.New("agenttest: DeploymentResolver conformance Resolver is nil")
	}
	if len(config.Resolvable) == 0 {
		return errors.New("agenttest: DeploymentResolver conformance requires at least one resolvable Deployment")
	}
	seen := make(map[agent.DeploymentRef]struct{}, len(config.Resolvable))
	for index, deployment := range config.Resolvable {
		if !deployment.Valid() {
			return fmt.Errorf("agenttest: DeploymentResolver conformance Resolvable[%d] is invalid", index)
		}
		reference := deployment.DeploymentRef()
		if _, duplicate := seen[reference]; duplicate {
			return fmt.Errorf("agenttest: DeploymentResolver conformance Resolvable[%d] repeats %s", index, reference)
		}
		seen[reference] = struct{}{}
	}
	for index, reference := range config.Unresolvable {
		if !reference.Valid() {
			return fmt.Errorf("agenttest: DeploymentResolver conformance Unresolvable[%d] is invalid", index)
		}
		if _, resolvable := seen[reference]; resolvable {
			return fmt.Errorf(
				"agenttest: DeploymentResolver conformance Unresolvable[%d] is also listed as resolvable", index,
			)
		}
	}
	return nil
}

// nameCollisionReference derives a reference that shares its name, contract,
// and implementation with deployment but froze different configuration. Only a
// resolver keyed by the exact reference rejects it.
func nameCollisionReference(deployment agent.Deployment) (agent.DeploymentRef, error) {
	reference := deployment.DeploymentRef()
	probe, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           deployment.Definition(),
		ImplementationDigest: reference.ImplementationDigest(),
		ConfigurationDigest: agent.ComputeDigest(
			[]byte("agenttest: deployment resolver name collision probe for " + reference.String()),
		),
	})
	if err != nil {
		return agent.DeploymentRef{}, fmt.Errorf(
			"agenttest: derive a name collision for %s: %w", reference.Name(), err,
		)
	}
	collision := probe.DeploymentRef()
	if collision == reference {
		return agent.DeploymentRef{}, fmt.Errorf(
			"agenttest: name collision probe for %s reproduced the original reference", reference.Name(),
		)
	}
	return collision, nil
}

func requireSameBinding(reference agent.DeploymentRef, want, got agent.Deployment) error {
	if !got.Valid() {
		return fmt.Errorf("agenttest: resolver returned an invalid Deployment for %s", reference.Name())
	}
	if got.DeploymentRef() != reference {
		return fmt.Errorf(
			"agenttest: resolver answered %s with %s", reference, got.DeploymentRef(),
		)
	}
	if got.Descriptor().Digest() != want.Descriptor().Digest() {
		return fmt.Errorf(
			"agenttest: resolver answered %s with a different Descriptor", reference,
		)
	}
	return nil
}

func callResolve(
	resolver agent.DeploymentResolver,
	reference agent.DeploymentRef,
) (deployment agent.Deployment, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			deployment = agent.Deployment{}
			err = &agent.CallbackPanicError{Operation: "DeploymentResolver.Resolve", Value: recovered}
		}
	}()
	return resolver.Resolve(reference)
}
