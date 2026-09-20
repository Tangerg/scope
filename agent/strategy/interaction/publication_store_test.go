package interaction_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
)

type storedResult struct {
	ProcessID agent.ProcessID
	Sequence  uint64
	Entry     interaction.ResultEntry
}

type publicationDatabase struct {
	Tree         agent.TreeSnapshot
	Publications map[string]json.RawMessage
	Transactions map[string]agent.Digest
	Results      []storedResult
}

// publicationStore models a host that chooses to keep a separate result history.
// The framework's committer contract requires only the authoritative tree cut.
type publicationStore struct {
	mu       sync.Mutex
	path     string
	closed   bool
	database publicationDatabase
	before   func(agent.TreeSnapshot, []interaction.RoundResults) error
	after    func(agent.TreeSnapshot, []interaction.RoundResults) error
}

func newPublicationStore(t *testing.T) *publicationStore {
	t.Helper()
	return openPublicationStore(t, filepath.Join(t.TempDir(), "publication.json"))
}

func openPublicationStore(t *testing.T, path string) *publicationStore {
	t.Helper()
	store := &publicationStore{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		store.database = publicationDatabase{Publications: make(map[string]json.RawMessage), Transactions: make(map[string]agent.Digest)}
	} else if err != nil {
		t.Fatal(err)
	} else if err := json.Unmarshal(data, &store.database); err != nil {
		t.Fatal(err)
	}
	return store
}

func (p *publicationStore) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.database = publicationDatabase{}
}

func (p *publicationStore) tree() agent.TreeSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.database.Tree
}

func (p *publicationStore) entries() []interaction.ResultEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	var entries []interaction.ResultEntry
	for _, result := range p.database.Results {
		entries = append(entries, result.Entry)
	}
	return entries
}

func (p *publicationStore) ActivateTree(_ context.Context, activation agent.TreeActivation) error {
	return p.commit(activation.TreeSnapshot(), activation.PreviousTreeDigest(), activation.PreviousIncarnationID(), "activation/"+activation.IncarnationID().String())
}

func (p *publicationStore) CommitEffect(_ context.Context, boundary agent.EffectBoundary) error {
	writer := boundary.TreeSnapshot().IncarnationID()
	return p.commit(boundary.TreeSnapshot(), boundary.PreviousTreeDigest(), writer, "effect/"+boundary.Request().ID().String()+"/"+boundary.Kind().String())
}

func (p *publicationStore) CommitCheckpoint(_ context.Context, checkpoint agent.TreeCheckpoint) error {
	writer := checkpoint.TreeSnapshot().IncarnationID()
	key := "checkpoint/" + checkpoint.TreeSnapshot().Digest().String()
	if checkpoint.Kind() == agent.TreeCheckpointKindStart {
		key = "start"
		writer = agent.TreeIncarnationID{}
	}
	return p.commit(checkpoint.TreeSnapshot(), checkpoint.PreviousTreeDigest(), writer, key)
}

func (p *publicationStore) commit(snapshot agent.TreeSnapshot, previous agent.Digest, expected agent.TreeIncarnationID, key string) error {
	publications, err := interaction.SettledResults(snapshot)
	if err != nil {
		return err
	}
	if p.before != nil {
		if beforeErr := p.before(snapshot, publications); beforeErr != nil {
			return beforeErr
		}
	}
	p.mu.Lock()
	err = p.advance(snapshot, previous, expected, key, publications)
	p.mu.Unlock()
	if err != nil {
		return err
	}
	if p.after != nil {
		return p.after(snapshot, publications)
	}
	return nil
}

func (p *publicationStore) advance(snapshot agent.TreeSnapshot, previous agent.Digest, expected agent.TreeIncarnationID, key string, publications []interaction.RoundResults) error {
	if p.closed {
		return os.ErrClosed
	}
	head := p.database.Tree
	writer := head.IncarnationID()
	proposedWriter := snapshot.IncarnationID()
	if writer != expected && (writer != proposedWriter || head.Digest() != snapshot.Digest()) {
		return agent.ErrTreeIncarnationConflict
	}
	if stored, exists := p.database.Transactions[key]; exists {
		if stored != snapshot.Digest() {
			return agent.ErrCommitConflict
		}
		if head.Digest() != snapshot.Digest() {
			return agent.ErrTreeIncarnationConflict
		}
		return nil
	}
	if head.Digest() != previous {
		return agent.ErrTreeIncarnationConflict
	}
	// The proposal is committed as one file replacement. No process memory is
	// needed after reopening to recover either the tree or its product projection.
	candidate := publicationDatabase{Tree: snapshot, Publications: make(map[string]json.RawMessage), Transactions: make(map[string]agent.Digest), Results: append([]storedResult(nil), p.database.Results...)}
	for id, data := range p.database.Publications {
		candidate.Publications[id] = data
	}
	for id, digest := range p.database.Transactions {
		candidate.Transactions[id] = digest
	}
	for _, publication := range publications {
		if err := candidate.recordPublication(publication.Digest(), publication.JSON()); err != nil {
			return err
		}
		for _, entry := range publication.Entries() {
			found := false
			for _, existing := range candidate.Results {
				if existing.ProcessID == publication.Relation().ProcessID() && existing.Sequence == publication.ModelCallSequence() && existing.Entry.ToolCallIndex == entry.ToolCallIndex {
					oldData, err := json.Marshal(existing.Entry)
					if err != nil {
						return err
					}
					newData, err := json.Marshal(entry)
					if err != nil {
						return err
					}
					if !bytes.Equal(oldData, newData) {
						return agent.ErrCommitConflict
					}
					found = true
					break
				}
			}
			if !found {
				candidate.Results = append(candidate.Results, storedResult{ProcessID: publication.Relation().ProcessID(), Sequence: publication.ModelCallSequence(), Entry: entry})
			}
		}
	}
	candidate.Transactions[key] = snapshot.Digest()
	data, err := json.Marshal(candidate)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(p.path), "transaction-")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(temporary, p.path); err != nil {
		return err
	}
	p.database = candidate
	return nil
}

func (p *publicationDatabase) recordPublication(id agent.Digest, payload json.RawMessage) error {
	if agent.ComputeDigest(payload) != id {
		return agent.ErrCommitConflict
	}
	if previous, found := p.Publications[id.String()]; found && !bytes.Equal(previous, payload) {
		return agent.ErrCommitConflict
	}
	p.Publications[id.String()] = bytes.Clone(payload)
	return nil
}

func publicationEngine(t *testing.T, deployment interactionDeployment, store agent.TreeCommitter) *agent.Engine {
	t.Helper()
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: deployment.resolver, TreeCommitter: store})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	return engine
}

func publicationText(entry interaction.ResultEntry) string {
	text, _ := entry.Result.Output.Text()
	return text
}
