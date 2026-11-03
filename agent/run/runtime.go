package run

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/samber/lo"
)

const defaultCheckpointInterval uint64 = 1024
const defaultStoreTimeout = 30 * time.Second

type Config struct {
	Store              Store
	Build              string
	ProgramResolver    ProgramResolver
	ProcessAdmitter    ProcessAdmitter
	Deltas             chan<- Delta
	Budget             Budget
	TreeLimits         TreeLimits
	Capabilities       CapabilitySet
	CheckpointInterval uint64
	StoreTimeout       time.Duration
}

type Runtime struct {
	store              Store
	compiler           *Compiler
	resolver           ProgramResolver
	admitter           ProcessAdmitter
	deltas             chan<- Delta
	budget             Budget
	limits             TreeLimits
	capabilities       CapabilitySet
	checkpointInterval uint64
	storeTimeout       time.Duration
	mu                 sync.RWMutex
	roots              map[ProcessID]*treeRuntime
	starting           map[string]*startup
	closeDone          chan struct{}
}

func New(ctx context.Context, config Config) (*Runtime, error) {
	if err := RequireContext(ctx).Err(); err != nil {
		return nil, err
	}
	if lo.IsNil(config.Store) || !ValidDescription(config.Build) || config.ProgramResolver != nil && lo.IsNil(config.ProgramResolver) || config.ProcessAdmitter != nil && lo.IsNil(config.ProcessAdmitter) || config.StoreTimeout < 0 {
		return nil, ErrInvalidConfig
	}
	limits, err := config.TreeLimits.resolve()
	if err != nil {
		return nil, err
	}
	interval := config.CheckpointInterval
	if interval == 0 {
		interval = defaultCheckpointInterval
	}
	timeout := config.StoreTimeout
	if timeout == 0 {
		timeout = defaultStoreTimeout
	}
	return &Runtime{store: config.Store, compiler: &Compiler{build: config.Build}, resolver: config.ProgramResolver, admitter: config.ProcessAdmitter, deltas: config.Deltas, budget: config.Budget, limits: limits, capabilities: config.Capabilities, checkpointInterval: interval, storeTimeout: timeout, roots: make(map[ProcessID]*treeRuntime), starting: make(map[string]*startup)}, nil
}

func (r *Runtime) Compiler() *Compiler {
	if r == nil {
		return nil
	}
	return r.compiler
}

func (r *Runtime) Start[I, O any](ctx context.Context, program Program[I, O], request StartRequest[I]) (*Handle[O], error) {
	if r == nil {
		return nil, ErrInvalidConfig
	}
	if !request.ID.Valid() {
		return nil, ErrInvalidStart
	}
	input, err := program.Descriptor().EncodeInput(request.Input)
	if err != nil {
		return nil, err
	}
	handle, err := r.start(ctx, context.WithoutCancel(RequireContext(ctx)), program.Erase(), request.ID, input)
	if err != nil {
		return nil, err
	}
	return typedHandle[O](handle), nil
}

func (r *Runtime) Run[I, O any](ctx context.Context, program Program[I, O], input I) (O, error) {
	var zero O
	if r == nil {
		return zero, ErrInvalidConfig
	}
	payload, err := program.Descriptor().EncodeInput(input)
	if err != nil {
		return zero, err
	}
	handle, err := r.start(ctx, ctx, program.Erase(), foregroundStartID(), payload)
	if err != nil {
		return zero, err
	}
	result, err := typedHandle[O](handle).Await(ctx)
	// Run owns the anonymous writer; only Start and Recover transfer that
	// lifetime to a Handle. The durable log remains available for recovery.
	if handle.tree != nil {
		handle.tree.stop()
		<-handle.tree.done
		r.mu.Lock()
		if r.roots[handle.id] == handle.tree {
			delete(r.roots, handle.id)
		}
		r.mu.Unlock()
	}
	if err != nil {
		if handle.tree != nil {
			return zero, handle.tree.interrupted(err)
		}
		return zero, err
	}
	return result.Output()
}

func (r *Runtime) start(ctx, work context.Context, program Program[Payload, Payload], id StartID, input Payload) (*Handle[Payload], error) {
	if err := RequireContext(ctx).Err(); err != nil {
		return nil, err
	}
	if err := r.validateProgram(program); err != nil {
		return nil, err
	}
	root := id.Root()
	claimed, release, err := r.claim(ctx, root)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx = claimed
	started := Started{Process: root, Program: program.ProgramRef(), Input: input, Budget: r.budget, Capabilities: r.capabilities, Limits: r.limits, At: canonicalTime(time.Now())}
	intent, err := started.IntentDigest()
	if err != nil {
		return nil, err
	}
	log, found, err := r.load(ctx, root)
	if err != nil {
		return nil, err
	}
	if found {
		return r.accepted(log, intent)
	}
	admission := ProcessAdmission{start: id, process: root, program: program.ProgramRef(), descriptor: program.descriptor, budget: r.budget, capabilities: r.capabilities}
	if admissionErr := requestProcessAdmission(ctx, r.admitter, admission); admissionErr != nil {
		return nil, admissionErr
	}
	execution, _, err := initializeExecution(ctx, program.definition, input)
	if err != nil {
		return nil, err
	}
	return r.launch(ctx, work, program, execution, started, intent)
}

// launch commits a new tree and starts its owner. Losing the commit race to
// another writer is not an error when that writer accepted the same intent.
func (r *Runtime) launch(ctx, work context.Context, program Program[Payload, Payload], execution Execution, started Started, intent Digest) (*Handle[Payload], error) {
	root := started.Process
	state := newTreeState(root)
	tree := newTreeRuntime(r, state, work)
	if err := tree.append(ctx, []Record{started, Activated{Epoch: 1}}); err != nil {
		tree.cancel()
		if appendConflict(err) {
			if current, exists, loadErr := r.load(ctx, root); loadErr == nil && exists {
				return r.accepted(current, intent)
			}
		}
		return nil, tree.interrupted(err)
	}
	p := state.processes[root]
	p.execution, p.program = execution, program
	if err := r.publish(tree); err != nil {
		tree.cancel()
		return nil, tree.interrupted(err)
	}
	handle := &Handle[Payload]{runtime: r, tree: tree, process: p, id: root, program: program.ProgramRef(), epoch: state.epoch}
	tree.enqueue(root)
	go tree.run()
	return handle, nil
}

func (r *Runtime) accepted(log Log, intent Digest) (*Handle[Payload], error) {
	if err := log.Validate(); err != nil {
		return nil, err
	}
	started, err := log.rootStarted()
	if err != nil {
		return nil, err
	}
	acceptedIntent, err := started.IntentDigest()
	if err != nil {
		return nil, err
	}
	if acceptedIntent != intent {
		return nil, &StartConflictError{Root: log.Root, Intent: acceptedIntent}
	}
	if local, ok := r.Process(log.Root); ok && local.Epoch() == log.Epoch() {
		return local, nil
	}
	return &Handle[Payload]{runtime: r, id: log.Root, program: started.Program, epoch: log.Epoch()}, nil
}

func (r *Runtime) Recover[I, O any](ctx context.Context, program Program[I, O], root ProcessID) (*Handle[O], error) {
	if r == nil {
		return nil, ErrInvalidConfig
	}
	if err := RequireContext(ctx).Err(); err != nil {
		return nil, err
	}
	if err := r.validateProgram(program.Erase()); err != nil {
		return nil, err
	}
	claimed, release, err := r.claim(ctx, root)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx = claimed
	r.mu.RLock()
	previous := r.roots[root]
	r.mu.RUnlock()
	if previous != nil {
		select {
		case <-previous.done:
		default:
			return nil, ErrActiveProcesses
		}
	}
	log, found, err := r.load(ctx, root)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrTreeNotFound
	}
	state, err := r.restore(ctx, program.Erase(), log)
	if err != nil {
		return nil, err
	}
	tree := newTreeRuntime(r, state, context.WithoutCancel(ctx))
	if err := tree.append(ctx, []Record{Activated{Epoch: state.epoch + 1}}); err != nil {
		tree.cancel()
		return nil, err
	}
	if err := r.publish(tree); err != nil {
		tree.cancel()
		return nil, err
	}
	handle := &Handle[O]{runtime: r, tree: tree, process: state.processes[root], id: root, program: program.ProgramRef(), epoch: state.epoch}
	tree.recovering = true
	for _, id := range state.ordered() {
		tree.enqueue(id)
	}
	go tree.run()
	return handle, nil
}

func (r *Runtime) Process(id ProcessID) (*Handle[Payload], bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	tree := r.roots[id.Root()]
	r.mu.RUnlock()
	if tree == nil {
		return nil, false
	}
	tree.mu.RLock()
	defer tree.mu.RUnlock()
	p := tree.state.processes[id]
	if p == nil {
		return nil, false
	}
	return &Handle[Payload]{runtime: r, tree: tree, process: p, id: id, program: p.started.Program, epoch: tree.state.epoch}, true
}

func (r *Runtime) Inspect(ctx context.Context, root ProcessID) (TreeInspection, error) {
	if r == nil {
		return TreeInspection{}, ErrInvalidConfig
	}
	if err := RequireContext(ctx).Err(); err != nil {
		return TreeInspection{}, err
	}
	log, found, err := r.load(ctx, root)
	if err != nil {
		return TreeInspection{}, err
	}
	if !found {
		return TreeInspection{}, ErrProcessNotFound
	}
	return log.Inspect()
}

func (r *Runtime) Close(ctx context.Context) error {
	if r == nil {
		return ErrInvalidConfig
	}
	ctx = RequireContext(ctx)
	r.mu.Lock()
	done := r.closeDone
	if done == nil {
		done = make(chan struct{})
		r.closeDone = done
		trees := make([]*treeRuntime, 0, len(r.roots))
		for _, tree := range r.roots {
			trees = append(trees, tree)
		}
		pending := make([]*startup, 0, len(r.starting))
		for _, start := range r.starting {
			pending = append(pending, start)
			start.cancel()
		}
		go func() {
			for _, tree := range trees {
				tree.stop()
			}
			for _, tree := range trees {
				<-tree.done
			}
			for _, start := range pending {
				<-start.done
			}
			close(done)
		}()
	}
	r.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runtime) ReleaseTree(ctx context.Context, root ProcessID) error {
	if r == nil {
		return ErrInvalidConfig
	}
	if err := RequireContext(ctx).Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	tree := r.roots[root]
	if tree == nil {
		return ErrTreeNotFound
	}
	select {
	case <-tree.done:
		delete(r.roots, root)
		return nil
	default:
		return ErrActiveProcesses
	}
}

func (r *Runtime) claim(ctx context.Context, root ProcessID) (context.Context, func(), error) {
	if !root.Valid() || root.Depth() != 0 {
		return nil, nil, ErrInvalidIdentity
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		r.mu.Lock()
		if r.closeDone != nil {
			r.mu.Unlock()
			return nil, nil, ErrClosed
		}
		pending := r.starting[root.String()]
		if pending == nil {
			work, cancel := context.WithCancel(ctx)
			pending = &startup{done: make(chan struct{}), cancel: cancel}
			r.starting[root.String()] = pending
			r.mu.Unlock()
			return work, func() {
				cancel()
				r.mu.Lock()
				delete(r.starting, root.String())
				close(pending.done)
				r.mu.Unlock()
			}, nil
		}
		r.mu.Unlock()
		select {
		case <-pending.done:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
}

func (r *Runtime) publish(tree *treeRuntime) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closeDone != nil {
		return ErrClosed
	}
	r.roots[tree.state.root] = tree
	return nil
}

func (r *Runtime) validateProgram(program Program[Payload, Payload]) error {
	if err := program.validateDefinition(); err != nil {
		return err
	}
	if program.ProgramRef().Build() != r.compiler.build {
		return fmt.Errorf("%w: program build differs from runtime build", ErrInvalidProgram)
	}
	return nil
}

func (r *Runtime) load(ctx context.Context, root ProcessID) (Log, bool, error) {
	ctx, cancel := context.WithTimeout(RequireContext(ctx), r.storeTimeout)
	defer cancel()
	type loaded struct {
		log   Log
		found bool
	}
	value, err := invokeCallback("Store.Load", func() (loaded, error) {
		log, found, err := r.store.Load(ctx, root)
		return loaded{log: log, found: found}, err
	})
	if err == nil && value.found && value.log.Root != root {
		return Log{}, false, ErrInvalidLog
	}
	return value.log, value.found, err
}

func (r *Runtime) read(ctx context.Context, root ProcessID, after LSN) ([]Entry, error) {
	var entries []Entry
	err := r.scan(ctx, root, after, func(entry Entry) bool {
		entries = append(entries, entry)
		return true
	})
	return entries, err
}

// scan visits root's history after an LSN until visit returns false, so a
// reader that knows where its facts end stops there instead of at the head.
func (r *Runtime) scan(ctx context.Context, root ProcessID, after LSN, visit func(Entry) bool) error {
	ctx, cancel := context.WithTimeout(RequireContext(ctx), r.storeTimeout)
	defer cancel()
	_, err := invokeCallback("Store.Read", func() (struct{}, error) {
		next := after + 1
		for entry, err := range r.store.Read(ctx, root, after) {
			if err != nil {
				return struct{}{}, err
			}
			if entry.LSN != next || !entry.belongsTo(root) {
				return struct{}{}, ErrInvalidLog
			}
			next++
			if !visit(entry) {
				break
			}
		}
		return struct{}{}, nil
	})
	return err
}

// Answered reports whether wait already admitted exactly this answer, so a
// caller can confirm a delivery whose acknowledgment was lost without sending
// it twice. A different answer to the same wait is ErrSignalConflict.
func (r *Runtime) Answered(ctx context.Context, process ProcessID, wait WaitKey, answer Payload) (bool, error) {
	if r == nil {
		return false, ErrInvalidConfig
	}
	receipts, _, err := r.receipts(ctx, process)
	if err != nil {
		return false, err
	}
	for _, receipt := range receipts {
		if receipt.Wait != wait {
			continue
		}
		if (Signal{ID: receipt.ID, Wait: wait, Payload: answer}).receipt() != receipt {
			return false, ErrSignalConflict
		}
		return true, nil
	}
	return false, nil
}

// receipts reads a process's admitted Signals from the recovery cut, or from
// history once reaping has removed the process from the cut, so an
// acknowledged delivery stays acknowledged for the life of the log.
func (r *Runtime) receipts(ctx context.Context, process ProcessID) (map[SignalID]SignalReceipt, bool, error) {
	state, err := r.committed(ctx, process.Root())
	if err != nil {
		return nil, false, err
	}
	if live := state.processes[process]; live != nil {
		return live.receipts, live.exited != nil, nil
	}
	subtree, started, found := state.reapedSubtree(process)
	if !found {
		return nil, false, ErrProcessNotFound
	}
	return r.reapedReceipts(ctx, process, subtree, started)
}

// reapedReceipts reads history only across the lifetime of the reaped subtree
// that contains process: from its Started record to its Reaped record.
func (r *Runtime) reapedReceipts(ctx context.Context, process, subtree ProcessID, started LSN) (map[SignalID]SignalReceipt, bool, error) {
	receipts := make(map[SignalID]SignalReceipt)
	found := false
	err := r.scan(ctx, process.Root(), started-1, func(entry Entry) bool {
		switch record := entry.Record.(type) {
		case Started:
			found = found || record.Process == process
		case SignalAdmitted:
			if record.Process == process {
				receipts[record.Signal.ID] = record.Signal.receipt()
			}
		case Reaped:
			return record.Process != process && record.Process != subtree
		}
		return true
	})
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, ErrProcessNotFound
	}
	return receipts, true, nil
}

// committed folds the recovery cut, the newest state any writer acknowledged.
func (r *Runtime) committed(ctx context.Context, root ProcessID) (*treeState, error) {
	log, found, err := r.load(ctx, root)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrTreeNotFound
	}
	return projectLog(log)
}

// History reads the complete log of root's tree, for projections that need
// facts a checkpoint no longer carries, such as settled tool rounds.
func (r *Runtime) History(ctx context.Context, root ProcessID) (Log, error) {
	if r == nil {
		return Log{}, ErrInvalidConfig
	}
	entries, err := r.read(ctx, root, 0)
	if err != nil {
		return Log{}, err
	}
	if len(entries) == 0 {
		return Log{}, ErrTreeNotFound
	}
	history := Log{Root: root, Entries: entries}
	return history, history.Validate()
}

func (r *Runtime) resolve(parent Program[Payload, Payload], reference ProgramRef) (Program[Payload, Payload], error) {
	if reference == parent.ProgramRef() {
		return parent, nil
	}
	if child, found := parent.boundChild(reference); found {
		return child, r.validateProgram(child)
	}
	if r.resolver == nil {
		return Program[Payload, Payload]{}, ErrInvalidProgram
	}
	program, err := invokeCallback("ProgramResolver.Resolve", func() (Program[Payload, Payload], error) { return r.resolver.Resolve(reference) })
	if err != nil {
		return Program[Payload, Payload]{}, err
	}
	if program.ProgramRef() != reference {
		return Program[Payload, Payload]{}, ErrInvalidProgram
	}
	return program, r.validateProgram(program)
}

// restore resumes from the Log's base and re-executes only the Steps after
// it, so recovery cost is bounded by the checkpoint interval rather than the
// tree's age. Each replayed Step must reproduce its committed Transition.
func (r *Runtime) restore(ctx context.Context, program Program[Payload, Payload], log Log) (*treeState, error) {
	started, err := log.rootStarted()
	if err != nil {
		return nil, err
	}
	if started.Program != program.ProgramRef() {
		return nil, ErrInvalidProgram
	}
	state, rest, err := log.base()
	if err != nil {
		return nil, err
	}
	if checkpoint, ok := log.Entries[0].Record.(Checkpoint); ok {
		if err := r.restoreCheckpoint(ctx, state, program, checkpoint); err != nil {
			return nil, err
		}
	}
	for _, entry := range rest {
		if err := r.replay(ctx, state, program, entry); err != nil {
			return nil, err
		}
	}
	if err := state.validateBoundary(); err != nil {
		return nil, err
	}
	return state, nil
}

func (r *Runtime) replay(ctx context.Context, state *treeState, program Program[Payload, Payload], entry Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if stepped, ok := entry.Record.(Stepped); ok {
		if err := state.verifyStep(ctx, stepped); err != nil {
			return err
		}
	}
	if err := state.apply(entry); err != nil {
		return err
	}
	started, ok := entry.Record.(Started)
	if !ok {
		return nil
	}
	process := state.processes[started.Process]
	if err := r.bindRestoredProcess(state, process, program); err != nil {
		return err
	}
	execution, _, err := initializeExecution(ctx, process.program.definition, started.Input)
	if err != nil {
		return err
	}
	process.execution = execution
	return nil
}

func (r *Runtime) bindRestoredProcess(state *treeState, process *processState, root Program[Payload, Payload]) error {
	bound := root
	if parentID, isChild := process.started.Process.Parent(); isChild {
		parent := state.processes[parentID]
		if parent == nil {
			return ErrInvalidLog
		}
		var err error
		bound, err = r.resolve(parent.program, process.started.Program)
		if err != nil {
			return err
		}
	}
	if bound.ProgramRef() != process.started.Program {
		return ErrInvalidProgram
	}
	if err := bound.descriptor.ValidateInput(process.started.Input); err != nil {
		return err
	}
	process.program = bound
	return nil
}

func (r *Runtime) restoreCheckpoint(ctx context.Context, state *treeState, root Program[Payload, Payload], checkpoint Checkpoint) error {
	processes := slices.Clone(checkpoint.Processes)
	// Parents must be bound before their live children. Reaped history needs no
	// implementation because its Steps are already covered by this checkpoint.
	slices.SortFunc(processes, func(a, b ProcessCheckpoint) int { return a.Started.Process.compare(b.Started.Process) })
	for _, captured := range processes {
		process := state.processes[captured.Started.Process]
		if err := r.bindRestoredProcess(state, process, root); err != nil {
			return err
		}
		if process.terminal.Valid() {
			continue
		}
		execution, err := restoreExecution(ctx, process.program.definition, captured.State)
		if err != nil {
			return err
		}
		process.execution = execution
	}
	return nil
}

type startup struct {
	done   chan struct{}
	cancel context.CancelFunc
}
