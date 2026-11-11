package luma

import (
	"errors"
	"fmt"
	"slices"

	"github.com/Tangerg/scope/core/httpclient"
)

// Provider-generated URLs are data, not a grant to use the host's network or
// cookies. Only the host can authorize output origins for this binding.
func outputOrigins(configured []string) (httpclient.Origins, error) {
	if len(configured) == 0 {
		return nil, errors.New("luma: OutputOrigins must authorize at least one output origin")
	}
	origins := make(httpclient.Origins, 0, len(configured))
	for index, raw := range configured {
		origin, err := httpclient.ParseOrigin(raw)
		if err != nil {
			return nil, fmt.Errorf("luma: OutputOrigins[%d]: %w", index, err)
		}
		if !slices.Contains(origins, origin) {
			origins = append(origins, origin)
		}
	}
	return origins, nil
}
