package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := run(ctx, os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, output io.Writer) error {
	for _, task := range contextTasks() {
		for _, policy := range []contextPolicy{
			{ID: unchangedContextPolicy}, {ID: duplicateOutputPolicyID, Reducer: duplicateToolOutputReducer{}},
		} {
			recorded, err := runTask(ctx, task, policy)
			if err != nil {
				return err
			}
			text, err := answer(recorded)
			if err != nil {
				return err
			}
			if text != task.Expected {
				return fmt.Errorf("%s/%s: answer failed the exact quality gate", task.ID, policy.ID)
			}
			bytes, err := inputBytes(recorded)
			if err != nil {
				return err
			}
			if _, writeErr := fmt.Fprintf(output, "%s/%s: quality=pass model_calls=%d input_bytes=%d\n", task.ID, policy.ID, len(recorded.ModelCalls()), bytes); writeErr != nil {
				return writeErr
			}
		}
	}
	return nil
}
