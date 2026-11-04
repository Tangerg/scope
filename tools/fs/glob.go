package fs

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/jsonschema"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

const globToolName = "glob"

// GlobRequest narrows the executor's immutable authority to a relative subtree;
// Path can never replace or broaden that root.
type GlobRequest struct {
	Pattern    string `json:"pattern" jsonschema_description:"Doublestar path pattern, such as **/*.go or src/**/*.ts."`
	Path       string `json:"path,omitempty" jsonschema_description:"Directory to search under. Defaults to the workspace root."`
	IgnoreCase bool   `json:"ignore_case,omitzero" jsonschema_description:"Match path components case-insensitively. Default false."`
	MaxResults int    `json:"max_results,omitzero" jsonschema_description:"Maximum paths to return. Defaults to 100."`
}

func (g GlobRequest) Validate() error {
	_, err := g.resultLimit()
	return err
}

func (g GlobRequest) resultLimit() (int, error) {
	limit, err := searchResultLimit(g.MaxResults, defaultGlobMaxResults)
	if err != nil {
		return 0, err
	}
	if len(g.Pattern) < minimumPatternLength {
		return 0, ErrEmptyPattern
	}
	if err := validateGlobPattern(g.Pattern); err != nil {
		return 0, err
	}
	return limit, nil
}

type GlobResponse struct {
	Paths     []string `json:"paths"`
	Truncated bool     `json:"truncated,omitzero"`
}

var _ toolcontract.Tool = (*GlobTool)(nil)

type GlobTool struct {
	function toolcontract.Func[GlobRequest, GlobResponse]
	executor Globber
}

func NewGlobTool(executor Globber) (*GlobTool, error) {
	if lo.IsNil(executor) {
		return nil, ErrNilExecutor
	}
	t := &GlobTool{executor: executor}
	typed, err := toolcontract.NewFunc(
		toolcontract.FuncConfig{
			Effect: toolcontract.EffectSafe,
			Name:   globToolName,
			Description: "List file paths matching a doublestar pattern. Use patterns such as **/*.go or src/**/*.ts. " +
				"Use grep to search file contents.",
		},
		t.glob,
	)
	if err != nil {
		return nil, fmt.Errorf("fs.NewGlobTool: %w", err)
	}
	t.function = typed
	return t, nil
}

func (g *GlobTool) ConcurrencyPolicy() func(toolcontract.Invocation) (string, bool) {
	return func(toolcontract.Invocation) (string, bool) { return "", true }
}

func (g *GlobTool) glob(ctx context.Context, req GlobRequest) (GlobResponse, error) {
	res, err := g.executor.Glob(ctx, req)
	if err != nil {
		return GlobResponse{}, lookupError("fs.glob", err)
	}
	return res, nil
}

func (GlobRequest) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Properties: map[string]jsonschema.Constraints{
		"pattern":     {MinLength: new(uint64(minimumPatternLength))},
		"max_results": {Minimum: new(float64(1)), Maximum: new(float64(maximumSearchResults))},
	}}
}

func (g *GlobTool) Definition() toolcontract.Definition { return g.function.Definition() }

func (g *GlobTool) InputValidator() func([]byte) error { return g.function.InputValidator() }

func (g *GlobTool) Call(ctx context.Context, invocation toolcontract.Invocation) (toolcontract.Result, error) {
	return g.function.Call(ctx, invocation)
}

func (g *GlobRequest) UnmarshalJSON(data []byte) error {
	if g == nil {
		return ErrInvalidInput
	}
	type wireRequest GlobRequest
	var decoded *wireRequest
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return err
	}
	if decoded == nil {
		return ErrInvalidInput
	}
	candidate := GlobRequest(*decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*g = candidate
	return nil
}
