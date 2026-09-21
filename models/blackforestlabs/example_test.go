package blackforestlabs_test

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/models/blackforestlabs"
)

func ExampleImageModelConfig() {
	options := image.Options{Model: blackforestlabs.ModelFlux11Pro, OutputFormat: "image/png"}
	_, err := blackforestlabs.NewImageModel(context.Background(), blackforestlabs.ImageModelConfig{
		APIKey: "example-key", DefaultOptions: options,
	})
	fmt.Println(options.Model, options.OutputFormat, err)
	// Output: flux-pro-1.1 image/png <nil>
}
