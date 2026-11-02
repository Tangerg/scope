package run

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/samber/lo"
)

var (
	ErrInvalidConfig         = errors.New("run: invalid runtime configuration")
	ErrClosed                = errors.New("run: runtime is closed")
	ErrQuiescenceUnavailable = errors.New("run: runtime cannot become quiescent")
	ErrActiveProcesses       = errors.New("run: runtime has active processes")
	ErrProcessAlreadyExists  = errors.New("run: process identity already exists")
	// ErrTreeNotFound reports that this Runtime has no registered root tree with
	// the requested identity, including after ReleaseTree removed it.
	ErrTreeNotFound = errors.New("run: tree not found")
)

// Config keeps scheduling and authority policy outside Programs so a
// strategy cannot change its constraints through its behavior binding. Budget,
// TreeLimits, and Capabilities apply to newly started root trees. Recover
// retains their captured values; the Host authorizes snapshots before recovery.
type Config struct {
	// Store is required. Publication follows acknowledgment of the
	// authoritative tree head, including when the Host selects volatile storage.
	Store Store

	// ProcessInitializationAcknowledger optionally accepts initialization outcomes
	// before publication. Its acknowledgment is separate from TreeCommitter;
	// nil omits this Host acceptance step.
	ProcessInitializationAcknowledger ProcessInitializationAcknowledger

	// ProgramResolver binds the exact child references a Program does
	// not own: children named by runtime input. A program referring to itself and
	// children a Program binds through Definition.ChildPrograms need no
	// resolver. Nil rejects every other child reference.
	ProgramResolver ProgramResolver

	// ProcessAdmitter optionally applies Host policy before root or child
	// initialization. Nil admits every start that satisfies Runtime constraints.
	ProcessAdmitter ProcessAdmitter

	// EventListeners receive queued, best-effort Framework facts. An empty slice disables
	// delivery. Callbacks must be bounded and must not query or control their tree.
	EventListeners []EventListener
	// EventBufferCapacity bounds pending callbacks per event listener. Zero uses 256.
	EventBufferCapacity int

	// DeltaListeners receive queued, best-effort Strategy increments. An empty
	// slice disables delivery. Callbacks run serially and must return so Runtime
	// shutdown can drain the queue and join its delivery worker.
	DeltaListeners []DeltaListener

	// Each delta listener has its own bounded queue.
	// Zero selects the library default; negative capacities are invalid.
	DeltaBufferCapacity int

	// Budget grants cumulative work authority to each new root; zero quotas are
	// unlimited.
	Budget Budget

	// TreeLimits is the capacity policy of each new root tree. Lifetime quotas
	// and snapshot bytes default to unlimited; depth, active-child, and
	// pending-Signal capacity inherit finite defaults.
	TreeLimits TreeLimits

	// Capabilities grants authority to new roots. Children receive only subsets
	// of their parent's captured authority, including after restoration.
	Capabilities CapabilitySet
}

// Runtime keeps admission, publication, and execution under one owner because
// resource reservations and recoverable tree state must describe the same
// lifecycle. Construct it with New; its zero value is not usable. A
// Runtime must not be copied because doing so shares its registries while
// duplicating their synchronization. Operations on a nil Runtime report
// ErrInvalidConfig; read accessors return zero values.
type Runtime struct {
	committer                  Store
	initializationAcknowledger ProcessInitializationAcknowledger
	resolver                   ProgramResolver
	admitter                   ProcessAdmitter
	observation                *observationBus
	budget                     Budget
	treeLimits                 TreeLimits
	capabilities               CapabilitySet

	// Tree-wide administrative operations use an independent serial lane so a
	// caller waiting for one root never holds the registry lock needed by Runtime
	// lifecycle paths. The two locks are therefore never nested.
	startMu          sync.Mutex
	submissions      map[StartID]chan struct{}
	treeOperationsMu sync.Mutex
	treeOperations   map[ProcessID]*treeOperation

	// Registry and reservation changes share this lock so publication cannot
	// expose a Process whose admission still appears unreserved.
	mu       sync.RWMutex
	registry processRegistry
	// One channel closes admission at allocation and joins completion on close,
	// avoiding separate shutdown flags that could disagree.
	closeDone chan struct{}
}

// ObservationFailures returns consistent panic counts and the latest bounded
// diagnostic for each listener kind. Listener failures cannot veto execution.
func (r *Runtime) ObservationFailures() ObservationFailures {
	if r == nil || r.observation == nil {
		return ObservationFailures{}
	}
	return r.observation.failureSnapshot()
}

// FlushObservations waits for all event and delta deliveries accepted before
// this call. It does not recover drops, block writers or wait for later events.
// Close rejects new flushes once shutdown begins.
func (r *Runtime) FlushObservations(ctx context.Context) error {
	if r == nil {
		return ErrInvalidConfig
	}
	ctx = RequireContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.observation.checkListenerJoin(ctx, "FlushObservations"); err != nil {
		return err
	}
	r.mu.RLock()
	closing := r.closeDone != nil
	r.mu.RUnlock()
	if closing {
		return ErrClosed
	}
	return r.observation.flush(ctx)
}

func (c Config) validateCollaborators() error {
	if lo.IsNil(c.Store) {
		return fmt.Errorf("%w: TreeCommitter is required", ErrInvalidConfig)
	}
	if c.ProcessInitializationAcknowledger != nil && lo.IsNil(c.ProcessInitializationAcknowledger) {
		return fmt.Errorf("%w: ProcessInitializationAcknowledger is typed nil", ErrInvalidConfig)
	}
	if c.ProgramResolver != nil && lo.IsNil(c.ProgramResolver) {
		return fmt.Errorf("%w: ProgramResolver is typed nil", ErrInvalidConfig)
	}
	if c.ProcessAdmitter != nil && lo.IsNil(c.ProcessAdmitter) {
		return fmt.Errorf("%w: ProcessAdmitter is typed nil", ErrInvalidConfig)
	}
	for index, listener := range c.EventListeners {
		if lo.IsNil(listener) {
			return fmt.Errorf("%w: EventListeners[%d] is nil", ErrInvalidConfig, index)
		}
	}
	for index, listener := range c.DeltaListeners {
		if lo.IsNil(listener) {
			return fmt.Errorf("%w: DeltaListeners[%d] is nil", ErrInvalidConfig, index)
		}
	}
	return nil
}

func New(ctx context.Context, config Config) (*Runtime, error) {
	if err := RequireContext(ctx).Err(); err != nil {
		return nil, err
	}
	if config.EventBufferCapacity < 0 {
		return nil, fmt.Errorf("%w: EventBufferCapacity must not be negative", ErrInvalidConfig)
	}
	if config.DeltaBufferCapacity < 0 {
		return nil, fmt.Errorf("%w: DeltaBufferCapacity must not be negative", ErrInvalidConfig)
	}
	if err := config.validateCollaborators(); err != nil {
		return nil, err
	}
	capacity := config.DeltaBufferCapacity
	if capacity == 0 {
		capacity = defaultObservationBuffer
	}
	treeLimits, err := config.TreeLimits.resolve()
	if err != nil {
		return nil, err
	}
	return &Runtime{
		committer:                  config.Store,
		initializationAcknowledger: config.ProcessInitializationAcknowledger,
		resolver:                   config.ProgramResolver,
		admitter:                   config.ProcessAdmitter,
		observation:                newObservationBus(config.EventListeners, config.DeltaListeners, cmp.Or(config.EventBufferCapacity, defaultObservationBuffer), capacity),
		budget:                     config.Budget,
		treeLimits:                 treeLimits,
		capabilities:               config.Capabilities,
		submissions:                make(map[StartID]chan struct{}),
		treeOperations:             make(map[ProcessID]*treeOperation),
		registry:                   newProcessRegistry(),
	}, nil
}

// Start keeps ctx attached to the resulting tree so Host cancellation and
// deadlines reach accepted work; execution that outlives a request needs a
// longer-lived context. An already-canceled context never reserves an identity
// or invokes Host admission. Insufficient Process or tree snapshot capacity,
// including mandatory lifecycle growth, returns ErrResourceLimitExceeded before
// the initial head commits. Later Steps, dispatch permissions, and settlements
// each require their own capacity admission.
func (r *Runtime) startTree(ctx, workCtx context.Context, deployment Program[Payload, Payload], input Payload, key StartID) (*Handle[Payload], error) {
	if r == nil {
		return nil, ErrInvalidConfig
	}
	ctx = RequireContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := deployment.validateDefinition(); err != nil {
		return nil, err
	}
	if err := deployment.Descriptor().ValidateInput(input); err != nil {
		return nil, err
	}
	id := newProcessID()
	relation := rootProcessRelation(id)
	admission := newProcessAdmission(relation, deployment, r.budget, r.capabilities)
	admission.startID = key
	if err := r.reserveProcessStart(relation); err != nil {
		return nil, err
	}
	published := false
	defer func() {
		if !published {
			r.discardProcessStart(relation)
		}
	}()
	if requestProcessAdmissionErr := requestProcessAdmission(ctx, r.admitter, admission); requestProcessAdmissionErr != nil {
		return nil, requestProcessAdmissionErr
	}
	execution, state, failure, err := initializeExecution(ctx, deployment.Definition(), input)
	// A root is admitted exactly once per Start and is never re-initialized on
	// recovery, so its outcome carries the zero incarnation; only a child start
	// can be re-attempted under a new incarnation.
	if err != nil {
		acknowledgeErr := acknowledgeProcessInitialization(ctx, r.initializationAcknowledger, failedProcessInitializationOutcome(admission, TreeIncarnationID{}, failure))
		return nil, errors.Join(fmt.Errorf("run: initialize Process: %w", err), acknowledgeErr)
	}
	if acknowledgeErr := acknowledgeProcessInitialization(ctx, r.initializationAcknowledger, initializedProcessOutcome(admission, TreeIncarnationID{})); acknowledgeErr != nil {
		return nil, acknowledgeErr
	}
	// The start time is stamped with the handle and persisted in the root's first
	// checkpoint, matching how a child's start time is stamped at publication.
	handle := newProcessHandle(relation, deployment, r.budget, r.capabilities, canonicalTime(time.Now()))
	process := newProcessState(handle, deployment, execution, state)
	runtime := newTreeRuntime(r, relation.RootID(), r.treeLimits, workCtx, process)

	if capacityErr := runtime.validateSnapshotCapacity(); capacityErr != nil {
		return nil, capacityErr
	}

	baseSnapshot, captureErr := runtime.captureTree()
	if captureErr != nil {
		return nil, captureErr
	}
	if err := runtime.writer.commitStart(ctx, baseSnapshot, StartReceipt{ID: key, RootID: id, Intent: startIntent(deployment.ProgramRef(), input)}); err != nil {
		// The initial checkpoint was attempted but not acknowledged. Surface the
		// allocated root and attempted-head identity so the Host can reconcile; an
		// attempted head is never presented as an acknowledged one.
		if errors.Is(err, ErrCommitConflict) || errors.Is(err, ErrTreeIncarnationConflict) {
			return nil, err
		}
		if _, ok := errors.AsType[*StartAcceptedError](err); ok {
			return nil, err
		}
		if _, ok := errors.AsType[*StartConflictError](err); ok {
			return nil, err
		}
		return nil, &StartError{
			key:         key,
			rootID:      relation.RootID(),
			incarnation: runtime.writer.incarnation(),
			sequence:    1,
			digest:      baseSnapshot.Digest(),
			err:         err,
		}
	}

	r.publishProcessStart(handle)
	published = true
	go runtime.run(workCtx)
	return &Handle[Payload]{handle: handle}, nil
}

// StartRequest binds a caller-known key to one immutable input. Share the
// key with a retry only when it denotes the same logical submission.
type StartRequest[I any] struct {
	ID    StartID
	Input I
}

// Start admits a managed tree. After admission, request cancellation stops no
// accepted work. The Host controls the tree explicitly through the issued Handle.
func (r *Runtime) Start[I, O any](ctx context.Context, program Program[I, O], req StartRequest[I]) (*Handle[O], error) {
	ctx = RequireContext(ctx)
	if r == nil {
		return nil, ErrInvalidConfig
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !req.ID.Valid() {
		return nil, ErrInvalidStart
	}
	if !program.Valid() {
		return nil, ErrInvalidProgram
	}
	release, err := r.claimStart(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	defer release()
	in, err := program.Descriptor().EncodeInput(req.Input)
	if err != nil {
		return nil, err
	}
	intent := startIntent(program.ProgramRef(), in)
	accepted := func(receipt StartReceipt) (*Handle[O], error) {
		if receipt.Intent != intent {
			return nil, &StartConflictError{Receipt: receipt}
		}
		local, registered := r.Process(receipt.RootID)
		reader, readable := r.committer.(HeadReader)
		if !registered || !readable {
			return nil, &StartAcceptedError{Receipt: receipt}
		}
		head, headErr := receipt.loadHead(ctx, reader)
		if headErr != nil {
			return nil, errors.Join(&StartAcceptedError{Receipt: receipt}, headErr)
		}
		if local.TreeIncarnationID() == head.IncarnationID() {
			return &Handle[O]{handle: local.handle}, nil
		}
		return nil, &StartAcceptedError{Receipt: receipt}
	}
	receipt, found, err := req.ID.lookup(ctx, r.committer)
	if err != nil {
		return nil, err
	}
	if found {
		return accepted(receipt)
	}
	p, err := r.startTree(ctx, context.WithoutCancel(ctx), program.Erase(), in, req.ID)
	if old, ok := errors.AsType[*StartAcceptedError](err); ok {
		if !old.Receipt.matches(req.ID) {
			return nil, ErrInvalidStart
		}
		return accepted(old.Receipt)
	}
	if err != nil {
		return nil, err
	}
	return &Handle[O]{handle: p.handle}, nil
}

// claimStart serializes local publication for one request key. Across
// Runtimes the atomic store receipt, not this short-lived lane, arbitrates.
func (r *Runtime) claimStart(ctx context.Context, id StartID) (func(), error) {
	for {
		r.startMu.Lock()
		active := r.submissions[id]
		if active == nil {
			active = make(chan struct{})
			r.submissions[id] = active
			r.startMu.Unlock()
			return func() { r.startMu.Lock(); delete(r.submissions, id); close(active); r.startMu.Unlock() }, nil
		}
		r.startMu.Unlock()
		select {
		case <-active:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Run binds execution cancellation to ctx and joins the entire local subtree
// before projecting its output. Transient waits never convert a timeout into
// proof that an external operation stopped.
func (r *Runtime) Run[I, O any](ctx context.Context, program Program[I, O], input I) (O, error) {
	var zero O
	if r == nil {
		return zero, ErrInvalidConfig
	}
	ctx = RequireContext(ctx)
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if !program.Valid() {
		return zero, ErrInvalidProgram
	}
	in, err := program.Descriptor().EncodeInput(input)
	if err != nil {
		return zero, err
	}
	p, err := r.startTree(ctx, ctx, program.Erase(), in, StartID{Scope: "foreground", Key: randomHexIdentity("call:", 16).String()})
	if err != nil {
		return zero, err
	}
	cleanup := context.WithoutCancel(ctx)
	err = p.Join(cleanup)
	if err != nil {
		return zero, errors.Join(err, r.ReleaseTree(cleanup, p.ID()))
	}
	result, err := p.Await(cleanup)
	release := r.ReleaseTree(cleanup, p.ID())
	if err != nil || release != nil {
		return zero, errors.Join(err, release)
	}
	return projectResult[O](result).Output()
}

func (r *Runtime) Process(id ProcessID) (*Handle[Payload], bool) {
	if r == nil || !id.Valid() {
		return nil, false
	}
	r.mu.RLock()
	handle := r.registry.entries[id].handle
	r.mu.RUnlock()
	if handle == nil {
		return nil, false
	}
	return &Handle[Payload]{handle: handle}, true
}

// Close rejects pending starts or restorations, Processes whose result
// publication or parent/child bookkeeping is incomplete, and trees that still
// own asynchronous work or a freeze. Handle.Join or Run establishes subtree
// completion; callers must finish every tree and release any freeze before
// closing the Runtime.
// Once closing begins, the Runtime drains accepted Delta delivery and stops
// observation workers. Canceling ctx stops only this caller's wait; it does not
// interrupt that owned shutdown, which later and concurrent callers join.
// Existing handles retain results and RuntimeErrors for later reads.
func (r *Runtime) Close(ctx context.Context) error {
	if r == nil {
		return ErrInvalidConfig
	}
	ctx = RequireContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.observation.checkListenerJoin(ctx, "Close"); err != nil {
		return err
	}
	done, err := r.startClose()
	if err != nil {
		return err
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runtime) startClose() (<-chan struct{}, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closeDone != nil {
		return r.closeDone, nil
	}
	for _, entry := range r.registry.entries {
		handle := entry.handle
		if handle == nil {
			return nil, fmt.Errorf("%w: Process publication is pending", ErrActiveProcesses)
		}
		select {
		case <-handle.bookkeepingDone:
		default:
			return nil, fmt.Errorf(
				"%w: Process %s has an unpublished outcome or pending parent/child bookkeeping",
				ErrActiveProcesses, handle.processID(),
			)
		}
	}
	for rootID := range r.registry.entries {
		if runtime := r.rootRuntime(rootID); runtime != nil && runtime.ownsActiveWork() {
			return nil, fmt.Errorf(
				"%w: tree %s still owns active work",
				ErrActiveProcesses, rootID,
			)
		}
	}
	done := make(chan struct{})
	r.closeDone = done
	// The Runtime owns shutdown independently of any caller's wait. Observation
	// delivery must be joined after releasing the registry lock used by listeners.
	go func() {
		r.observation.close()
		close(done)
	}()
	return done, nil
}

func (r *Runtime) reserveProcessStart(relation ProcessRelation) error {
	if !relation.Valid() {
		return ErrInvalidProcessRelation
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closeDone != nil {
		return ErrClosed
	}
	return r.registry.reserve(relation, nil)
}

func (r *Runtime) discardProcessStart(relation ProcessRelation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registry.discard(relation, nil)
}

func (r *Runtime) publishProcessStart(handle *processHandle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closeDone != nil || handle.treeRuntime() == nil {
		panic("run: invalid Process start reservation")
	}
	r.registry.publish(handle, nil)
}

// InspectTree samples a registered root tree without freezing it or waiting
// for storage acknowledgment. It remains available during a commit, while a
// freeze is acquired or held, and after the owner stops or Runtime.Close returns.
// ReleaseTree removes the lookup; an overlapping inspection may return its
// earlier valid sample. ctx bounds both admission and response waiting.
// The owner never calls user execution code to construct a report. Callbacks
// cannot control their own tree with the active listener context. Query failures are returned as errors; runtime
// failures are reported through ProcessInspection.RuntimeError.
func (r *Runtime) InspectTree(ctx context.Context, rootID ProcessID) (TreeInspection, error) {
	if r == nil {
		return TreeInspection{}, ErrInvalidConfig
	}
	ctx = RequireContext(ctx)
	if err := ctx.Err(); err != nil {
		return TreeInspection{}, err
	}
	if err := r.observation.checkEventListenerReentrancy(ctx, rootID, "InspectTree"); err != nil {
		return TreeInspection{}, err
	}
	runtime, err := r.runtimeForTree(rootID)
	if err != nil {
		return TreeInspection{}, err
	}
	return runtime.inspect(ctx)
}

func (r *Runtime) acquireTreeOperation(
	ctx context.Context,
	rootID ProcessID,
) (*treeOperation, error) {
	ctx = RequireContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := r.observation.checkEventListenerReentrancy(ctx, rootID, "tree operation"); err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r.treeOperationsMu.Lock()
		active := r.treeOperations[rootID]
		if active == nil {
			operation := &treeOperation{
				engine: r, rootID: rootID, released: make(chan struct{}),
			}
			r.treeOperations[rootID] = operation
			r.treeOperationsMu.Unlock()
			return operation, nil
		}
		released := active.released
		r.treeOperationsMu.Unlock()
		select {
		case <-released:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// ReleaseTree waits for the complete root tree to settle and removes its
// in-memory registry entries and execution state. It does not cancel work or
// delete Host persistence. Capture any required TreeSnapshot before releasing.
// Existing Process handles retain their Result or RuntimeError;
// Runtime.Process no longer finds the released identities, and tree operations
// return ErrTreeNotFound.
// Canceling ctx before settlement leaves the tree registered and usable.
func (r *Runtime) ReleaseTree(ctx context.Context, rootID ProcessID) error {
	if r == nil {
		return ErrInvalidConfig
	}
	ctx = RequireContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.observation.checkEventListenerReentrancy(ctx, rootID, "ReleaseTree"); err != nil {
		return err
	}
	runtime, err := r.runtimeForTree(rootID)
	if err != nil {
		return err
	}
	select {
	case <-runtime.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	// Settlement may need another tree operation, so acquire exclusive access
	// only after the runtime has stopped.
	operation, err := r.acquireTreeOperation(ctx, rootID)
	if err != nil {
		return err
	}
	defer operation.release()
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rootRuntime(rootID) != runtime {
		return ErrTreeNotFound
	}
	for _, process := range runtime.members.all() {
		r.registry.release(process.handle)
	}
	runtime.binding.Store(nil)
	return nil
}

// Recover recreates a complete Process tree from one strict TreeSnapshot.
// The supplied program must exactly match the captured root. Children bound by the
// root's Program, transitively through their own bindings, reuse those
// bindings; only other exact references are resolved through the configured
// ProgramResolver. Registration is all-or-nothing within this Runtime.
// The Runtime reserves every captured identity before resolving children or
// restoring Execution state. Any failure releases the entire reservation.
// Committed states and nonterminal prepared candidates must restore through
// their exact Definition before registration, activation, or Effect dispatch.
// Interrupted terminal candidates remain inert evidence and are not restored.
// Both completed and prepared completion outputs must satisfy that Definition's
// output schema before admission.
//
// Snapshot capabilities, limits, budgets, and usage remain authoritative. Current
// Config start defaults do not revoke or rewrite captured grants. The Host
// must authorize the snapshot before calling Recover; ProcessAdmitter and
// initialization acknowledgment are not repeated for captured Processes.
// Captured capacity must fit lifecycle reservations for live Processes and the
// encoded size of terminal Processes. Restoration rejects insufficient capacity
// before activating a writer or publishing handles. Later growth requires new
// capacity admission.
func (r *Runtime) Recover[I, O any](
	ctx context.Context,
	rootDeployment Program[I, O],
	snapshot TreeSnapshot,
) (*Handle[O], error) {
	if r == nil {
		return nil, ErrInvalidConfig
	}
	ctx = RequireContext(ctx)
	restoration, err := r.newRestoration(rootDeployment.Erase(), snapshot)
	if err != nil {
		return nil, err
	}
	operation, err := r.acquireTreeOperation(ctx, restoration.wire.rootID())
	if err != nil {
		return nil, err
	}
	defer operation.release()
	// Admission precedes the prepare pass so a closed Runtime or a taken
	// identity refuses without running Host Definition code.
	if err = r.reserveRestoredTree(restoration); err != nil {
		return nil, err
	}
	published := false
	defer func() {
		if !published {
			r.discardRestoredTree(restoration)
		}
	}()
	if err = restoration.prepare(ctx); err != nil {
		return nil, err
	}
	prospectiveSnapshot, err := restoration.runtime.writer.activate(ctx, snapshot, restoration.wire)
	if err != nil {
		return nil, err
	}
	restoration.wire = prospectiveSnapshot.state

	r.publishRestoredTree(restoration)
	published = true
	p := r.startRestoredTree(context.WithoutCancel(ctx), restoration)
	return &Handle[O]{handle: p.handle}, nil
}

// ValidateRestorableTree reports whether this Runtime can rebuild snapshot under
// the supplied root program, using the same validation as Recover: exact program
// bindings, committed and prepared Execution states, mailbox and wait history,
// Signal and output schemas, and captured snapshot capacity.
//
// It skips admission, reserves no identity, and activates no writer, so it
// neither fences the previous writer nor promises that a later Recover
// succeeds. Retained Unknown settlements are restorable facts; whether to
// reconcile, replay, or refuse them stays a Host decision.
func (r *Runtime) ValidateRestorableTree(
	ctx context.Context,
	rootDeployment Program[Payload, Payload],
	snapshot TreeSnapshot,
) error {
	if r == nil {
		return ErrInvalidConfig
	}
	ctx = RequireContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	restoration, err := r.newRestoration(rootDeployment, snapshot)
	if err != nil {
		return err
	}
	return restoration.prepare(ctx)
}

func (r *Runtime) newRestoration(
	rootDeployment Program[Payload, Payload],
	snapshot TreeSnapshot,
) (*treeRestoration, error) {
	if err := rootDeployment.validateDefinition(); err != nil {
		return nil, err
	}
	wire, err := snapshot.wire()
	if err != nil {
		return nil, err
	}
	rootSnapshot := wire.processSnapshot(wire.rootID())
	if !rootSnapshot.Valid() || rootSnapshot.ProgramRef() != rootDeployment.ProgramRef() {
		return nil, fmt.Errorf("%w: exact root Program does not match", ErrInvalidTreeSnapshot)
	}
	restoration := &treeRestoration{engine: r, wire: wire, deployments: make(map[ProgramRef]Program[Payload, Payload])}
	if err := restoration.bind(rootDeployment); err != nil {
		return nil, err
	}
	return restoration, nil
}

func (r *Runtime) startRestoredTree(ctx context.Context, restoration *treeRestoration) *Handle[Payload] {
	runtime := restoration.runtime
	processes := runtime.members.ordered()
	for _, process := range processes {
		if process.status().Terminal() {
			process.handle.publishResult(process.result())
		}
	}
	for _, process := range slices.Backward(processes) {
		if process.status().Terminal() {
			runtime.propagateProcessTermination(process)
			runtime.finishProcessBookkeeping(process)
		}
	}
	root := runtime.members.get(runtime.rootID).handle
	go runtime.run(RequireContext(ctx))
	return &Handle[Payload]{handle: root}
}

func (r *Runtime) reserveRestoredTree(restoration *treeRestoration) error {
	if restoration == nil || len(restoration.wire.ProcessSnapshots) == 0 {
		return ErrInvalidTreeSnapshot
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closeDone != nil {
		return ErrClosed
	}
	for index, process := range restoration.wire.ProcessSnapshots {
		if err := r.registry.reserve(process.Relation(), restoration); err != nil {
			for _, claimed := range restoration.wire.ProcessSnapshots[:index] {
				r.registry.discard(claimed.Relation(), restoration)
			}
			return err
		}
	}
	return nil
}

func (r *Runtime) discardRestoredTree(restoration *treeRestoration) {
	if restoration == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, process := range restoration.wire.ProcessSnapshots {
		r.registry.discard(process.Relation(), restoration)
	}
}

func (r *Runtime) publishRestoredTree(restoration *treeRestoration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	runtime := restoration.runtime
	if r.closeDone != nil || runtime == nil || runtime.rootID != restoration.wire.rootID() ||
		runtime.members.len() != len(restoration.wire.ProcessSnapshots) {
		panic("run: invalid restored tree runtime")
	}
	// Verify the whole set before making any handle visible.
	for _, process := range runtime.members.all() {
		if !r.registry.reserved(process.handle.relation, restoration) {
			panic("run: restored Process reservation changed")
		}
	}
	for _, process := range runtime.members.all() {
		r.registry.publish(process.handle, restoration)
	}
}

// CaptureTree quiesces the tree at Strategy-safe boundaries, commits the cut,
// and returns that acknowledged snapshot. In-flight Effects settle before the
// barrier completes. After a runtime failure, read the authoritative head from
// the Host's commit store; unacknowledged runtime state is never recoverable.
func (r *Runtime) CaptureTree(ctx context.Context, rootID ProcessID) (TreeSnapshot, error) {
	if r == nil {
		return TreeSnapshot{}, ErrInvalidConfig
	}
	ctx = RequireContext(ctx)
	if !rootID.Valid() {
		return TreeSnapshot{}, ErrTreeNotFound
	}

	operation, err := r.acquireTreeOperation(ctx, rootID)
	if err != nil {
		return TreeSnapshot{}, err
	}
	defer operation.release()
	runtime, err := r.runtimeForTree(rootID)
	if err != nil {
		return TreeSnapshot{}, err
	}
	freeze, snapshot, err := runtime.acquireTreeFreeze(ctx)
	if err != nil {
		return TreeSnapshot{}, err
	}
	if freeze != nil {
		if releaseErr := freeze.release(); releaseErr != nil {
			return TreeSnapshot{}, releaseErr
		}
	}
	return snapshot, nil
}

func (r *Runtime) runtimeForTree(rootID ProcessID) (*treeRuntime, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	runtime := r.rootRuntime(rootID)
	if runtime == nil {
		return nil, ErrTreeNotFound
	}
	return runtime, nil
}

// rootRuntime requires r.mu. A registered root handle owns its tree's runtime.
func (r *Runtime) rootRuntime(rootID ProcessID) *treeRuntime {
	root := r.registry.entries[rootID].handle
	if root == nil || !root.relation.IsRoot() {
		return nil
	}
	return root.treeRuntime()
}

// StartedTrees returns a view of this runtime's own store. Requiring the
// capability before serving a protocol prevents admitting unobservable work.
func (r *Runtime) StartedTrees() (*StartedTrees, error) {
	if r == nil {
		return nil, ErrInvalidConfig
	}
	reader, ok := r.committer.(HeadReader)
	if !ok {
		return nil, fmt.Errorf("run: store cannot load acknowledged heads: %w", errors.ErrUnsupported)
	}
	return &StartedTrees{starts: r.committer, heads: reader}, nil
}
