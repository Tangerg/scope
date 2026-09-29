//go:build unix

package process_test

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Tangerg/scope/tools/process"
)

func ExampleProcess() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stdin, requests, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	defer stdin.Close()
	defer requests.Close()
	responses, stdout, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	defer responses.Close()
	defer stdout.Close()
	command, err := process.New(ctx, process.Config{
		Argv:      []string{"/bin/sh", "-c", `while IFS= read -r request; do printf 'response:%s\n' "$request"; done`},
		Directory: ".", Stdin: stdin, Stdout: stdout,
	})
	if err != nil {
		panic(err)
	}
	if startErr := command.Start(); startErr != nil {
		panic(startErr)
	}
	defer command.Close()
	// The parent owns these pipe handles; the command inherited its own copies.
	if closeErr := stdin.Close(); closeErr != nil {
		panic(closeErr)
	}
	if closeErr := stdout.Close(); closeErr != nil {
		panic(closeErr)
	}
	scanner := bufio.NewScanner(responses)
	for _, request := range []string{"initialize", "ping"} {
		if _, writeErr := fmt.Fprintln(requests, request); writeErr != nil {
			panic(writeErr)
		}
		if !scanner.Scan() {
			panic("missing response")
		}
		fmt.Println(scanner.Text())
	}
	if closeErr := requests.Close(); closeErr != nil {
		panic(closeErr)
	}
	result, err := command.Wait()
	if err != nil {
		panic(err)
	}
	fmt.Println("exit:", result.ExitCode)
	// Output:
	// response:initialize
	// response:ping
	// exit: 0
}
