package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/Tangerg/scope/models/catalog/internal/generator"
)

const failureExitCode = 1

func main() {
	source := flag.String("source", generator.DefaultSource, "models.dev api.json URL or local file path")
	ladders := flag.String("ladders", generator.DefaultLadderSource, "catwalk providers URL or local file path")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := generator.Generate(ctx, generator.Config{Source: *source, Ladders: *ladders, Directory: "."}); err != nil {
		fmt.Fprintln(os.Stderr, "catalog generator:", err)
		os.Exit(failureExitCode)
	}
}
