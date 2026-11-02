//go:build integration

package postgres

import (
	"bufio"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/agent/run"
)

const (
	workerModeEnvironment   = "SCOPE_RUN_APPEND_WORKER"
	workerSchemaEnvironment = "SCOPE_RUN_APPEND_SCHEMA"
	workerBatchEnvironment  = "SCOPE_RUN_APPEND_BATCH"
	workerBeforeCommit      = "before_commit"
	workerAfterCommit       = "after_commit"
	workerFenced            = "fenced_writer"
	workerReady             = "scope-run-worker-ready"
)

func TestPostgresProcessCrashKeepsLogAndHostWritesAtomic(t *testing.T) {
	for _, mode := range []string{workerBeforeCommit, workerAfterCommit} {
		t.Run(mode, func(t *testing.T) {
			fixture := newPostgresFixture(t)
			batch := postgresInitialBatch(t)
			process := startAppendProcess(t, fixture, batch, mode)
			process.awaitReady(t)
			store := fixture.store(t, func(batch run.Batch) (Mutation, error) {
				return Mutation{Apply: func(ctx context.Context, writer Writer) error {
					_, err := writer.Exec(ctx, "INSERT INTO business_records(root,after_lsn,payload) VALUES($1,$2,$3)", batch.Root.String(), uint64(batch.After), "retry")
					return err
				}}, nil
			})
			_, before, err := store.Load(t.Context(), batch.Root)
			if err != nil {
				t.Fatal(err)
			}
			if before != (mode == workerAfterCommit) {
				t.Fatalf("visibility before crash=%t", before)
			}
			if killErr := process.command.Process.Kill(); killErr != nil {
				t.Fatal(killErr)
			}
			if waitErr := process.wait(); waitErr == nil {
				t.Fatal("worker survived kill")
			}
			_, found, err := store.Load(t.Context(), batch.Root)
			if err != nil || found != (mode == workerAfterCommit) {
				t.Fatalf("crash log found=%t error=%v", found, err)
			}
			wantRows := 0
			if mode == workerAfterCommit {
				wantRows = 1
			}
			if rows := fixture.businessRows(t); rows != wantRows {
				t.Fatalf("crash business rows=%d want=%d", rows, wantRows)
			}
			if err := store.Append(t.Context(), batch); err != nil {
				t.Fatal(err)
			}
			if fixture.businessRows(t) != 1 {
				t.Fatal("retry duplicated or omitted Host writes")
			}
		})
	}
}

func TestIndependentProcessLosesItsWriterEpoch(t *testing.T) {
	fixture := newPostgresFixture(t)
	store := fixture.store(t, nil)
	initial := postgresInitialBatch(t)
	if err := store.Append(t.Context(), initial); err != nil {
		t.Fatal(err)
	}
	id, err := run.ParseSignalID("stale")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := run.EncodePayload("stale")
	if err != nil {
		t.Fatal(err)
	}
	started := initial.Records[0].(run.Started)
	stale := run.Batch{Root: initial.Root, Epoch: 1, After: 2, Records: []run.Record{run.SignalAdmitted{Process: initial.Root, Signal: run.Signal{ID: id, Payload: payload}, At: started.At}}}
	process := startAppendProcess(t, fixture, stale, workerFenced)
	process.awaitReady(t)
	activation := run.Batch{Root: initial.Root, Epoch: 1, After: 2, Records: []run.Record{run.Activated{Epoch: 2}}}
	if appendErr := store.Append(t.Context(), activation); appendErr != nil {
		t.Fatal(appendErr)
	}
	if _, writeErr := process.input.Write([]byte("continue\n")); writeErr != nil {
		t.Fatal(writeErr)
	}
	if waitErr := process.wait(); waitErr != nil {
		t.Fatal(waitErr)
	}
	log, found, err := store.Load(t.Context(), initial.Root)
	if err != nil || !found || log.Head() != 3 || log.Epoch() != 2 {
		t.Fatalf("fenced log=%+v found=%t error=%v", log, found, err)
	}
}

func TestAppendProcessWorker(t *testing.T) {
	mode := os.Getenv(workerModeEnvironment)
	if mode == "" {
		return
	}
	data, err := os.ReadFile(os.Getenv(workerBatchEnvironment))
	if err != nil {
		t.Fatal(err)
	}
	var batch run.Batch
	if decodeErr := jsonv2.Unmarshal(data, &batch); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	config, err := pgxpool.ParseConfig(os.Getenv(databaseEnvironment))
	if err != nil {
		t.Fatal(err)
	}
	schema := os.Getenv(workerSchemaEnvironment)
	config.ConnConfig.RuntimeParams["search_path"] = pgx.Identifier{schema}.Sanitize()
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	awaitParent := func() error {
		if _, writeErr := fmt.Fprintln(os.Stdout, workerReady); writeErr != nil {
			return writeErr
		}
		_, readErr := bufio.NewReader(os.Stdin).ReadString('\n')
		return readErr
	}
	prepare := func(batch run.Batch) (Mutation, error) {
		return Mutation{Apply: func(ctx context.Context, writer Writer) error {
			if _, writeErr := writer.Exec(ctx, "INSERT INTO business_records(root,after_lsn,payload) VALUES($1,$2,$3)", batch.Root.String(), uint64(batch.After), mode); writeErr != nil {
				return writeErr
			}
			if mode == workerBeforeCommit {
				return awaitParent()
			}
			return nil
		}}, nil
	}
	store, err := New(t.Context(), Config{Pool: pool, Schema: schema, Timeout: storageTimeout, Prepare: prepare})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if mode == workerFenced {
		if err := awaitParent(); err != nil {
			t.Fatal(err)
		}
		if err := store.Append(t.Context(), batch); !errors.Is(err, run.ErrEpochConflict) {
			t.Fatalf("stale writer result=%v", err)
		}
		return
	}
	if err := store.Append(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	if mode == workerAfterCommit {
		if err := awaitParent(); err != nil {
			t.Fatal(err)
		}
	}
}

type appendProcess struct {
	command *exec.Cmd
	input   io.WriteCloser
	ready   chan error
	waited  bool
}

func startAppendProcess(t *testing.T, fixture *postgresFixture, batch run.Batch, mode string) *appendProcess {
	t.Helper()
	data, err := jsonv2.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "batch.json")
	if writeErr := os.WriteFile(path, data, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), binary, "-test.run=^TestAppendProcessWorker$", "-test.v")
	command.Env = append(os.Environ(), workerModeEnvironment+"="+mode, workerSchemaEnvironment+"="+fixture.schema, workerBatchEnvironment+"="+path)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = command.Stdout
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	process := &appendProcess{command: command, input: input, ready: make(chan error, 1)}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !process.waited {
			if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Error(err)
			}
			var exitError *exec.ExitError
			if err := process.wait(); err != nil && !errors.As(err, &exitError) {
				t.Error(err)
			}
		}
		if err := input.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
	})
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) == workerReady {
				process.ready <- nil
				return
			}
		}
		if err := scanner.Err(); err != nil {
			process.ready <- err
			return
		}
		process.ready <- errors.New("postgres: append worker exited before its crash barrier")
	}()
	return process
}

func (a *appendProcess) awaitReady(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), storageTimeout)
	defer cancel()
	select {
	case err := <-a.ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
func (a *appendProcess) wait() error { a.waited = true; return a.command.Wait() }
