package interaction

import (
	"context"
	"fmt"
	"strings"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/tool"
)

// AdvertisingTool is a Tool that reveals already-bound deferred Tools to the
// model after it runs under an Interaction. When a managed call settles with a
// successful result, the Interaction calls AdvertiseDeferred with that call's
// invocation and result and makes the returned deferred Tool names visible from
// the next model call onward. The capability lives in the Tool's type, not in
// the call context: a Tool that does not implement it never advertises, the same
// Tool run outside an Interaction has no advertising authority, and a rejected,
// failed, or input-pending call never reaches AdvertiseDeferred, so its
// declaration cannot commit. Advertising adds visibility only, never executable
// authority. Names must be exact deferred Tool names; the Interaction normalizes
// order and duplicates and rejects any name that is not a bound deferred Tool.
type AdvertisingTool interface {
	AdvertiseDeferred(ctx context.Context, invocation tool.Invocation, result tool.Result) ([]string, error)
}

// advertisingCapability resolves the AdvertisingTool capability through the
// Tool's wrapping chain. A capability-detection panic is an internal fault, not
// a model-visible result, so it is classified and returned as an error.
func advertisingCapability(executable tool.Tool) (advertiser AdvertisingTool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			advertiser = nil
			err = &agent.CallbackPanicError{Operation: "AdvertisingTool capability", Value: recovered}
		}
	}()
	capability, found, err := tool.Capability[AdvertisingTool](executable)
	if err != nil {
		return nil, fmt.Errorf("advertising capability: %w", err)
	}
	if !found {
		return nil, nil
	}
	return capability, nil
}

func validateAdvertisedToolNames(names []string) error {
	seen := make(map[string]struct{}, len(names))
	for index, name := range names {
		if name == "" || strings.TrimSpace(name) != name {
			return fmt.Errorf("advertised Tool name %d is empty or has surrounding whitespace", index)
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("advertised Tool name %q is duplicated", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}
