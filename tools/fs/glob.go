package fs

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/tools/internal/toolinput"

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
	return toolinput.Rejection(g.validate())
}

func (g GlobRequest) validate() error {
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

// globFunc is embedded under an unexported name so the tool keeps its
// Func private while promoting the Tool methods.
type globFunc = toolcontract.Func[GlobRequest, GlobResponse]

type GlobTool struct {
	globFunc
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
	t.globFunc = typed
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
