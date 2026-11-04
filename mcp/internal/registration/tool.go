package registration

import (
	"fmt"

	sdk "github.com/Tangerg/go-sdk/mcp"
)

type Tool struct {
	Descriptor *sdk.Tool
	Handler    sdk.ToolHandler
}

// The SDK's descriptor validation is available only through AddTool, which
// panics on invalid protocol annotations. An unconnected server runs that same
// admission without publishing a partial catalog or owning any sessions.
// Descriptors are private snapshots shared by admission and installation.
func Register(server *sdk.Server, tools ...Tool) error {
	staging := sdk.NewServer(&sdk.Implementation{}, nil)
	for _, tool := range tools {
		if err := tool.admit(staging); err != nil {
			return err
		}
	}
	for _, tool := range tools {
		server.AddTool(tool.Descriptor, tool.Handler)
	}
	return nil
}

func (t Tool) admit(server *sdk.Server) (err error) {
	defer func() {
		switch failure := recover().(type) {
		case nil:
		case error:
			err = fmt.Errorf("mcp: tool registration: %w", failure)
		default:
			err = fmt.Errorf("mcp: tool registration panicked: %v", failure)
		}
	}()
	server.AddTool(t.Descriptor, t.Handler)
	return nil
}
