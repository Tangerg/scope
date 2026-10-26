package agent

import (
	"errors"
	"testing"
)

func TestTreeRestorationRejectsInexactResolverAndTransitiveBindingDrift(t *testing.T) {
	cause := errors.New("deployment registry unavailable")
	for _, scenario := range []string{"registry failure", "different binding", "changed grandchild", "shared grandchild"} {
		t.Run(scenario, func(t *testing.T) {
			leaf := newEngineTestDefinition(t, "test.restore.leaf", "wait")
			liveLeaf := &descriptorBoundaryDefinition{Definition: leaf, descriptor: leaf.Descriptor}
			leafBinding := engineTestDeployment(t, liveLeaf, nil)
			middle := engineTestDeployment(t, &boundDefinition{
				Definition: newEngineTestDefinition(t, "test.restore.middle", "wait"), children: []Deployment{leafBinding},
			}, nil)
			rootChildren := []Deployment{middle}
			if scenario == "shared grandchild" {
				rootChildren = append(rootChildren, leafBinding)
			}
			root := engineTestDeployment(t, &boundDefinition{
				Definition: newEngineTestDefinition(t, "test.restore.root", "wait"), children: rootChildren,
			}, nil)
			if scenario == "changed grandchild" {
				liveLeaf.descriptor = func() Descriptor { return Descriptor{} }
			}
			calls := 0
			resolver := deploymentResolverFunc(func(reference DeploymentRef) (Deployment, error) {
				calls++
				if reference != root.DeploymentRef() {
					return Deployment{}, errors.New("unexpected external resolution for a bound child")
				}
				switch scenario {
				case "registry failure":
					return Deployment{}, cause
				case "different binding":
					return middle, nil
				default:
					return root, nil
				}
			})
			restoration := &treeRestoration{engine: &Engine{resolver: resolver}, deployments: make(map[DeploymentRef]Deployment)}
			deployment, err := restoration.deployment(root.DeploymentRef())
			if scenario != "shared grandchild" {
				if deployment.Valid() || !errors.Is(err, ErrInvalidTreeSnapshot) {
					t.Fatalf("unusable binding was accepted: %v", err)
				}
				if scenario == "registry failure" && !errors.Is(err, cause) {
					t.Fatalf("registry cause was lost: %v", err)
				}
				if scenario != "registry failure" && !errors.Is(err, ErrInvalidDeployment) {
					t.Fatalf("binding mismatch was misclassified: %v", err)
				}
				return
			}
			if err != nil || deployment.DeploymentRef() != root.DeploymentRef() {
				t.Fatalf("valid transitive binding rejected: %v", err)
			}
			for _, want := range []Deployment{root, middle, leafBinding} {
				got, err := restoration.deployment(want.DeploymentRef())
				if err != nil || got.DeploymentRef() != want.DeploymentRef() || got.Definition() != want.Definition() {
					t.Fatalf("bound descendant lost its exact owner: %v", err)
				}
			}
			if calls != 1 || len(restoration.deployments) != 3 {
				t.Fatalf("shared descendant was resolved twice: registry calls=%d bindings=%d", calls, len(restoration.deployments))
			}
		})
	}
}
