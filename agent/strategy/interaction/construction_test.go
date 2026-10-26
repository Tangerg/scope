package interaction

import (
	"context"
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/tool"
)

func TestInteractionConstructionRejectsUnboundToolPolicyAndInvalidDelegates(t *testing.T) {
	delegate := fuzzInteractionDefinition(t).delegates[0]
	for _, test := range []struct {
		name   string
		change func(*DefinitionConfig)
	}{
		{"invalid name", func(c *DefinitionConfig) { c.Name = "Invalid Name" }},
		{"invalid description", func(c *DefinitionConfig) { c.Description = "" }},
		{"unbound tool budget", func(c *DefinitionConfig) { c.ToolBudget.Steps = agent.NewQuota(1) }},
		{"unbound scheduling", func(c *DefinitionConfig) { c.MaxConcurrentToolCalls = 2 }},
		{"invalid delegate", func(c *DefinitionConfig) { c.Delegates = []Delegate{{}} }},
		{"duplicate delegate", func(c *DefinitionConfig) { c.Delegates = []Delegate{delegate, delegate} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := DefinitionConfig{Name: "interaction.construction", Description: "Validate exact child authority."}
			test.change(&config)
			if definition, err := NewDefinition(config); !errors.Is(err, ErrInvalidDefinitionConfig) || definition != nil {
				t.Fatalf("invalid definition = %v, %v", definition, err)
			}
		})
	}
	for _, definition := range []*Definition{nil, {}} {
		if definition.Descriptor().Valid() || len(definition.ChildDeployments()) != 0 {
			t.Fatal("absent definition exposed a binding")
		}
		if _, err := definition.Start(agent.Payload{}); !errors.Is(err, ErrInvalidDefinitionConfig) {
			t.Fatal(err)
		}
		if _, err := definition.Restore(t.Context(), agent.ExecutionState{}); !errors.Is(err, ErrInvalidDefinitionConfig) {
			t.Fatal(err)
		}
	}
}

func TestToolSetConstructionRequiresExecutableAuthorityAndArtifactIdentity(t *testing.T) {
	executable, err := tool.NewFunc(tool.FuncConfig{Name: "inspect", Description: "Inspect a fixed local value."}, func(context.Context, struct{}) (string, error) { return "value", nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*ToolSetConfig)
	}{
		{"no tools", func(c *ToolSetConfig) { c.Tools = nil }},
		{"typed nil observer", func(c *ToolSetConfig) { c.Observer = (*panickingExecutionObserver)(nil) }},
		{"invalid name", func(c *ToolSetConfig) { c.Name = "Invalid Name" }},
		{"missing implementation identity", func(c *ToolSetConfig) { c.ImplementationDigest = agent.Digest{} }},
		{"missing configuration identity", func(c *ToolSetConfig) { c.ConfigurationDigest = agent.Digest{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := ToolSetConfig{Name: "interaction.construction.tools", Description: "Bind one exact local tool.", Tools: []tool.Tool{executable}, ImplementationDigest: agent.ComputeDigest([]byte("implementation")), ConfigurationDigest: agent.ComputeDigest([]byte("configuration"))}
			test.change(&config)
			if set, err := NewToolSet(config); !errors.Is(err, ErrInvalidToolSet) || set.Configured() || set.Deployment().Valid() {
				t.Fatalf("invalid tool authority = %+v, %v", set, err)
			}
		})
	}
	var absent ToolSet
	if absent.Configured() || absent.Deployment().Valid() || absent.ObservationFailures().ToolStartedPanics() != 0 {
		t.Fatal("absent tool authority reports a live binding")
	}
}
