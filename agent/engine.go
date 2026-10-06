package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/samber/lo"
)

var (
	ErrInvalidEngineConfig         = errors.New("agent: invalid engine configuration")
	ErrEngineClosed                = errors.New("agent: engine is closed")
	ErrEngineQuiescenceUnavailable = errors.New("agent: engine cannot become quiescent")
	ErrEngineHasActiveProcesses    = errors.New("agent: engine has active processes")
	ErrProcessAlreadyExists        = errors.New("agent: process identity already exists")
	// ErrTreeNotFound reports that this Engine has no registered root tree with
	// the requested identity, including after ReleaseTree removed it.
	ErrTreeNotFound = errors.New("agent: tree not found")
)

// EngineConfig keeps scheduling and authority policy outside Deployments so a
// strategy cannot change its constraints through its behavior binding. Budget,
// TreeLimits, and Capabilities apply to newly started root trees. RestoreTree
// retains their captured values; the Host authorizes snapshots before recovery.
type EngineConfig struct {
	// TreeCommitter is required. Publication follows acknowledgment of the
	// authoritative tree head, including when the Host selects volatile storage.
	TreeCommitter TreeCommitter

	// ProcessInitializationAcknowledger optionally accepts initialization outcomes
	// before publication. Its acknowledgment is separate from TreeCommitter;
	// nil omits this Host acceptance step.
	ProcessInitializationAcknowledger ProcessInitializationAcknowledger

	// DeploymentResolver binds the exact child references a Deployment does
	// not own: children named by runtime input. Same-Deployment recursion and
	// children a Deployment binds through Definition.ChildDeployments need no
	// resolver. Nil rejects every other child reference.
	DeploymentResolver DeploymentResolver

	// ProcessAdmitter optionally applies Host policy before root or child
	// initialization. Nil admits every start that satisfies Engine constraints.
	ProcessAdmitter ProcessAdmitter

	// EventListeners receive synchronous Framework facts. An empty slice disables
	// delivery. Callbacks must be bounded and must not query or control their tree.
	EventListeners []EventListener

	// DeltaListeners receive queued, best-effort Strategy increments. An empty
	// slice disables delivery. Callbacks run serially and must return so Engine
	// shutdown can drain the queue and join its delivery worker.
	DeltaListeners []DeltaListener

	// A bounded queue prevents slow listeners from retaining unlimited Deltas.
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

// Engine keeps admission, publication, and execution under one owner because
// resource reservations and recoverable tree state must describe the same
// lifecycle. Construct it with NewEngine; its zero value is not usable. An
// Engine must not be copied because doing so shares its registries while
// duplicating their synchronization. Operations on a nil Engine report
// ErrInvalidEngineConfig; read accessors return zero values.
type Engine struct {
	committer                  TreeCommitter
	initializationAcknowledger ProcessInitializationAcknowledger
	resolver                   DeploymentResolver
	admitter                   ProcessAdmitter
	observation                *observationBus
	budget                     Budget
	treeLimits                 TreeLimits
	capabilities               CapabilitySet

	// Tree-wide administrative operations use an independent serial lane so a
	// caller waiting for one root never holds the registry lock needed by Engine
	// lifecycle paths. The two locks are therefore never nested.
	treeOperationsMu sync.Mutex
	treeOperations   map[ProcessID]*treeOperation

	// Registry and reservation changes share this lock so publication cannot
	// expose a Process whose admission still appears unreserved.
	mu                      sync.RWMutex
	processes               map[ProcessID]*processHandle
	startReservations       map[ProcessID]struct{}
	treeRestoreReservations map[ProcessID]*treeRestoration
	// These indexes project the same reservation and change only under mu.
	restoredProcesses      map[ProcessID]*treeRestoration
	restoredChildren       map[childIdentity]*treeRestoration
	children               map[childIdentity]ProcessID
	childStartReservations map[childIdentity]struct{}
	// One channel closes admission at allocation and joins completion on close,
	// avoiding separate shutdown flags that could disagree.
	closeDone chan struct{}
}

// ObservationFailures returns consistent panic counts and the latest bounded
// diagnostic for each listener kind. Listener failures cannot veto execution.
func (e *Engine) ObservationFailures() ObservationFailures {
	if e == nil || e.observation == nil {
		return ObservationFailures{}
	}
	return e.observation.failureSnapshot()
}

// FlushDeltas provides the ordering barrier needed before publishing a final
// value that must not overtake accepted streaming observations. Dropped Deltas
// remain lost because flushing cannot strengthen best-effort delivery. Closing
// the Engine rejects flushes whose admission check observes closure. A flush
// already admitted may race with Close, even with no listeners configured.
func (e *Engine) FlushDeltas(ctx context.Context) error {
	if e == nil {
		return ErrInvalidEngineConfig
	}
	ctx = RequireContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.observation.checkDeltaListenerReentrancy(ctx, "FlushDeltas"); err != nil {
		return err
	}
	e.mu.RLock()
	closing := e.closeDone != nil
	e.mu.RUnlock()
	if closing {
		return ErrEngineClosed
	}
	return e.observation.flushDeltas(ctx)
}

func (e EngineConfig) validateCollaborators() error {
	if lo.IsNil(e.TreeCommitter) {
		return fmt.Errorf("%w: TreeCommitter is required", ErrInvalidEngineConfig)
	}
	if e.ProcessInitializationAcknowledger != nil && lo.IsNil(e.ProcessInitializationAcknowledger) {
		return fmt.Errorf("%w: ProcessInitializationAcknowledger is typed nil", ErrInvalidEngineConfig)
	}
	if e.DeploymentResolver != nil && lo.IsNil(e.DeploymentResolver) {
		return fmt.Errorf("%w: DeploymentResolver is typed nil", ErrInvalidEngineConfig)
	}
	if e.ProcessAdmitter != nil && lo.IsNil(e.ProcessAdmitter) {
		return fmt.Errorf("%w: ProcessAdmitter is typed nil", ErrInvalidEngineConfig)
	}
	for index, listener := range e.EventListeners {
		if lo.IsNil(listener) {
			return fmt.Errorf("%w: EventListeners[%d] is nil", ErrInvalidEngineConfig, index)
		}
	}
	for index, listener := range e.DeltaListeners {
		if lo.IsNil(listener) {
			return fmt.Errorf("%w: DeltaListeners[%d] is nil", ErrInvalidEngineConfig, index)
		}
	}
	return nil
}

func NewEngine(config EngineConfig) (*Engine, error) {
	if config.DeltaBufferCapacity < 0 {
		return nil, fmt.Errorf("%w: DeltaBufferCapacity must not be negative", ErrInvalidEngineConfig)
	}
	if err := config.validateCollaborators(); err != nil {
		return nil, err
	}
	capacity := config.DeltaBufferCapacity
	if capacity == 0 {
		capacity = defaultDeltaBuffer
	}
	treeLimits, err := config.TreeLimits.resolve()
	if err != nil {
		return nil, err
	}
	if !config.Capabilities.Valid() {
		return nil, fmt.Errorf("%w: capabilities are invalid", ErrInvalidEngineConfig)
	}
	return &Engine{
		committer:                  config.TreeCommitter,
		initializationAcknowledger: config.ProcessInitializationAcknowledger,
		resolver:                   config.DeploymentResolver,
		admitter:                   config.ProcessAdmitter,
		observation:                newObservationBus(config.EventListeners, config.DeltaListeners, capacity),
		budget:                     config.Budget,
		treeLimits:                 treeLimits,
		capabilities:               config.Capabilities,
		treeOperations:             make(map[ProcessID]*treeOperation),
		processes:                  make(map[ProcessID]*processHandle),
		startReservations:          make(map[ProcessID]struct{}),
		treeRestoreReservations:    make(map[ProcessID]*treeRestoration),
		restoredProcesses:          make(map[ProcessID]*treeRestoration),
		restoredChildren:           make(map[childIdentity]*treeRestoration),
		children:                   make(map[childIdentity]ProcessID),
		childStartReservations:     make(map[childIdentity]struct{}),
	}, nil
}

// Start keeps ctx attached to the resulting tree so Host cancellation and
// deadlines reach accepted work; execution that outlives a request needs a
// longer-lived context. An already-canceled context never reserves an identity
// or invokes Host admission. Insufficient Process or tree snapshot capacity,
// including mandatory lifecycle growth, returns ErrResourceLimitExceeded before
// the initial head commits. Later Steps, dispatch permissions, and settlements
// each require their own capacity admission.
func (e *Engine) Start(ctx context.Context, deployment Deployment, input Payload) (*Process, error) {
	if e == nil {
		return nil, ErrInvalidEngineConfig
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
	admission := newProcessAdmission(relation, deployment, e.budget, e.capabilities)
	if err := e.reserveProcessStart(relation); err != nil {
		return nil, err
	}
	published := false
	defer func() {
		if !published {
			e.discardProcessStart(relation)
		}
	}()
	if requestProcessAdmissionErr := requestProcessAdmission(ctx, e.admitter, admission); requestProcessAdmissionErr != nil {
		return nil, requestProcessAdmissionErr
	}
	startedAt := canonicalTime(time.Now())
	execution, state, failure, err := initializeExecution(ctx, deployment.Definition(), input)
	if err != nil {
		acknowledgeErr := acknowledgeProcessInitialization(ctx, e.initializationAcknowledger, failedProcessInitializationOutcome(admission, failure))
		return nil, errors.Join(fmt.Errorf("agent: initialize Process: %w", err), acknowledgeErr)
	}
	if acknowledgeErr := acknowledgeProcessInitialization(ctx, e.initializationAcknowledger, initializedProcessOutcome(admission, startedAt)); acknowledgeErr != nil {
		return nil, acknowledgeErr
	}
	handle := newProcessHandle(relation, deployment, e.budget, e.capabilities, startedAt)
	process := newProcessState(handle, execution, state)
	runtime := newTreeRuntime(e, relation.RootID(), e.treeLimits, ctx, process)

	if capacityErr := runtime.validateSnapshotCapacity(); capacityErr != nil {
		return nil, capacityErr
	}

	baseSnapshot, captureErr := runtime.captureTree()
	if captureErr != nil {
		return nil, captureErr
	}
	if err := runtime.writer.commitStart(ctx, baseSnapshot); err != nil {
		return nil, err
	}

	e.publishProcessStart(handle)
	published = true
	go runtime.run(ctx)
	return &Process{handle: handle}, nil
}

// Run starts one root Process and joins its entire subtree before returning the
// root's terminal Result. It keeps waiting after ctx cancellation because owned
// work and required acknowledgments must finish. A runtime failure anywhere in
// the subtree returns a RuntimeError and no Result, even when the root already
// completed; its acknowledged result remains available through Process.Await.
// Ordinary execution failure returns the root's valid Result and nil error.
func (e *Engine) Run(ctx context.Context, deployment Deployment, input Payload) (Result, error) {
	process, err := e.Start(ctx, deployment, input)
	if err != nil {
		return Result{}, err
	}
	waitContext := context.WithoutCancel(RequireContext(ctx))
	if err := process.Join(waitContext); err != nil {
		return Result{}, err
	}
	return process.Await(waitContext)
}

// Process finds a published Process.
func (e *Engine) Process(id ProcessID) (*Process, bool) {
	if e == nil || !id.Valid() {
		return nil, false
	}
	e.mu.RLock()
	handle, exists := e.processes[id]
	e.mu.RUnlock()
	if !exists {
		return nil, false
	}
	return &Process{handle: handle}, true
}

// Close rejects pending starts or restorations, Processes whose result
// publication or parent/child bookkeeping is incomplete, and trees that still
// own asynchronous work or a freeze. Process.Join or Run establishes subtree
// completion; callers must finish every tree and release any freeze before
// closing the Engine.
// Once closing begins, the Engine drains accepted Delta delivery and stops
// observation workers. Canceling ctx stops only this caller's wait; it does not
// interrupt that owned shutdown, which later and concurrent callers join.
// Existing handles retain results and RuntimeErrors for later reads.
func (e *Engine) Close(ctx context.Context) error {
	if e == nil {
		return ErrInvalidEngineConfig
	}
	ctx = RequireContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.observation.checkDeltaListenerReentrancy(ctx, "Close"); err != nil {
		return err
	}
	done, err := e.startClose()
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

func (e *Engine) startClose() (<-chan struct{}, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closeDone != nil {
		return e.closeDone, nil
	}
	if len(e.startReservations) != 0 || len(e.treeRestoreReservations) != 0 {
		return nil, fmt.Errorf("%w: Process publication is pending", ErrEngineHasActiveProcesses)
	}
	for _, handle := range e.processes {
		select {
		case <-handle.bookkeepingDone:
		default:
			return nil, fmt.Errorf(
				"%w: Process %s has an unpublished outcome or pending parent/child bookkeeping",
				ErrEngineHasActiveProcesses, handle.processID(),
			)
		}
	}
	for rootID := range e.processes {
		if runtime := e.rootRuntime(rootID); runtime != nil && runtime.ownsActiveWork() {
			return nil, fmt.Errorf(
				"%w: tree %s still owns active work",
				ErrEngineHasActiveProcesses, rootID,
			)
		}
	}
	done := make(chan struct{})
	e.closeDone = done
	// The Engine owns shutdown independently of any caller's wait. Observation
	// delivery must be joined after releasing the registry lock used by listeners.
	go func() {
		e.observation.close()
		close(done)
	}()
	return done, nil
}

func (e *Engine) reserveProcessStart(relation ProcessRelation) error {
	if !relation.Valid() {
		return ErrInvalidProcessRelation
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closeDone != nil {
		return ErrEngineClosed
	}
	if e.processIdentityTaken(relation.ProcessID()) {
		return ErrProcessAlreadyExists
	}
	if relation.IsRoot() {
		e.startReservations[relation.ProcessID()] = struct{}{}
		return nil
	}
	return e.reserveChildStart(relation)
}

// processIdentityTaken and childIdentityTaken require e.mu. Published,
// starting, and restoring Processes reserve the same identity space.
func (e *Engine) processIdentityTaken(processID ProcessID) bool {
	_, published := e.processes[processID]
	_, starting := e.startReservations[processID]
	return published || starting || e.restoredProcesses[processID] != nil
}

func (e *Engine) childIdentityTaken(identity childIdentity) bool {
	_, published := e.children[identity]
	_, starting := e.childStartReservations[identity]
	return published || starting || e.restoredChildren[identity] != nil
}

// The tree owner admits resources and depth; e.mu reserves global identities
// atomically.
func (e *Engine) reserveChildStart(relation ProcessRelation) error {
	identity, isChild := relation.childIdentity()
	if !isChild {
		return ErrInvalidProcessRelation
	}
	if e.childIdentityTaken(identity) {
		return ErrInvalidChildStart
	}
	if e.processes[identity.parent] == nil {
		return ErrInvalidProcessRelation
	}
	e.startReservations[relation.ProcessID()] = struct{}{}
	e.childStartReservations[identity] = struct{}{}
	return nil
}

// discardProcessStart releases the identities relation's start reserved.
func (e *Engine) discardProcessStart(relation ProcessRelation) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, reserved := e.startReservations[relation.ProcessID()]; !reserved {
		return
	}
	delete(e.startReservations, relation.ProcessID())
	if identity, child := relation.childIdentity(); child {
		delete(e.childStartReservations, identity)
	}
}

func (e *Engine) publishProcessStart(handle *processHandle) {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, reserved := e.startReservations[handle.processID()]
	if !reserved || e.closeDone != nil ||
		e.processes[handle.processID()] != nil {
		panic("agent: invalid Process start reservation")
	}
	identity, isChild := handle.relation.childIdentity()
	if isChild {
		if _, reserved := e.childStartReservations[identity]; !reserved || e.children[identity].Valid() ||
			e.processes[identity.parent] == nil {
			panic("agent: invalid child Process start reservation")
		}
	}
	if handle.treeRuntime() == nil {
		panic("agent: Process start has no tree runtime")
	}
	delete(e.startReservations, handle.processID())
	e.processes[handle.processID()] = handle
	if isChild {
		delete(e.childStartReservations, identity)
		e.children[identity] = handle.processID()
	}
}

// InspectTree samples a registered root tree without freezing it or waiting
// for storage acknowledgment. It remains available during a commit, while a
// freeze is acquired or held, and after the owner stops or Engine.Close returns.
// ReleaseTree removes the lookup; an overlapping inspection may return its
// earlier valid sample. ctx bounds both admission and response waiting.
// The owner never calls user execution code to construct a report. Dependencies
// must still return in bounded time: synchronous EventListeners must not query
// or control their own tree. Query failures are returned as errors; runtime
// failures are reported through ProcessInspection.RuntimeError.
func (e *Engine) InspectTree(ctx context.Context, rootID ProcessID) (TreeInspection, error) {
	if e == nil {
		return TreeInspection{}, ErrInvalidEngineConfig
	}
	ctx = RequireContext(ctx)
	if err := ctx.Err(); err != nil {
		return TreeInspection{}, err
	}
	if err := e.observation.checkEventListenerReentrancy(ctx, rootID, "InspectTree"); err != nil {
		return TreeInspection{}, err
	}
	runtime, err := e.runtimeForTree(rootID)
	if err != nil {
		return TreeInspection{}, err
	}
	return runtime.inspect(ctx)
}

func (e *Engine) acquireTreeOperation(
	ctx context.Context,
	rootID ProcessID,
) (*treeOperation, error) {
	ctx = RequireContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := e.observation.checkEventListenerReentrancy(ctx, rootID, "tree operation"); err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		e.treeOperationsMu.Lock()
		active := e.treeOperations[rootID]
		if active == nil {
			operation := &treeOperation{
				engine: e, rootID: rootID, released: make(chan struct{}),
			}
			e.treeOperations[rootID] = operation
			e.treeOperationsMu.Unlock()
			return operation, nil
		}
		released := active.released
		e.treeOperationsMu.Unlock()
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
// Engine.Process no longer finds the released identities, and tree operations
// return ErrTreeNotFound.
// Canceling ctx before settlement leaves the tree registered and usable.
func (e *Engine) ReleaseTree(ctx context.Context, rootID ProcessID) error {
	if e == nil {
		return ErrInvalidEngineConfig
	}
	ctx = RequireContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.observation.checkEventListenerReentrancy(ctx, rootID, "ReleaseTree"); err != nil {
		return err
	}
	runtime, err := e.runtimeForTree(rootID)
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
	operation, err := e.acquireTreeOperation(ctx, rootID)
	if err != nil {
		return err
	}
	defer operation.release()
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.rootRuntime(rootID) != runtime {
		return ErrTreeNotFound
	}
	for processID, process := range runtime.members.all() {
		if identity, child := process.handle.relation.childIdentity(); child {
			delete(e.children, identity)
		}
		delete(e.processes, processID)
	}
	runtime.binding.Store(nil)
	return nil
}

// RestoreTree recreates a complete Process tree from one strict TreeSnapshot.
// rootDeployment must exactly bind the captured root. Children bound by the
// root's Deployment, transitively through their own bindings, reuse those
// bindings; only other exact references are resolved through EngineConfig's
// DeploymentResolver. Registration is all-or-nothing within this Engine.
// The Engine reserves every captured identity before resolving children or
// restoring Execution state. Any failure releases the entire reservation.
// Committed states and nonterminal prepared candidates must restore through
// their exact Definition before registration, activation, or Effect dispatch.
// Interrupted terminal candidates remain inert evidence and are not restored.
// Both completed and prepared completion outputs must satisfy that Definition's
// output schema before admission.
//
// Snapshot capabilities, limits, budgets, and usage remain authoritative. Current
// EngineConfig start defaults do not revoke or rewrite captured grants. The Host
// must authorize the snapshot before calling RestoreTree; ProcessAdmitter and
// initialization acknowledgment are not repeated for captured Processes.
// Captured capacity must fit lifecycle reservations for live Processes and the
// encoded size of terminal Processes. Restoration rejects insufficient capacity
// before activating a writer or publishing handles. Later growth requires new
// capacity admission.
func (e *Engine) RestoreTree(
	ctx context.Context,
	rootDeployment Deployment,
	snapshot TreeSnapshot,
) (*Process, error) {
	if e == nil {
		return nil, ErrInvalidEngineConfig
	}
	ctx = RequireContext(ctx)
	restoration, err := e.newRestoration(rootDeployment, snapshot)
	if err != nil {
		return nil, err
	}
	operation, err := e.acquireTreeOperation(ctx, restoration.wire.rootID())
	if err != nil {
		return nil, err
	}
	defer operation.release()
	// Admission precedes the prepare pass so a closed Engine or a taken
	// identity refuses without running Host Definition code.
	if err = e.reserveRestoredTree(restoration); err != nil {
		return nil, err
	}
	published := false
	defer func() {
		if !published {
			e.discardRestoredTree(restoration)
		}
	}()
	if err = restoration.prepare(ctx); err != nil {
		return nil, err
	}
	writer := restoration.runtime.writer
	wire := restoration.wire
	wire.IncarnationID = writer.incarnation()
	prospectiveSnapshot, err := newTreeSnapshot(wire)
	if err != nil {
		return nil, err
	}
	if err = writer.activate(ctx, snapshot, prospectiveSnapshot); err != nil {
		return nil, err
	}
	restoration.wire = wire

	e.publishRestoredTree(restoration)
	published = true
	return e.startRestoredTree(ctx, restoration), nil
}

// ValidateRestorableTree reports whether this Engine can rebuild snapshot under
// rootDeployment, using the same prepare pass as RestoreTree: exact Deployment
// bindings, committed and prepared Execution states, mailbox and wait history,
// Signal and output schemas, and captured snapshot capacity.
//
// It skips admission, reserves no identity, and activates no writer, so it
// neither fences the previous writer nor promises that a later RestoreTree
// succeeds. Retained Unknown settlements are restorable facts; whether to
// reconcile, replay, or refuse them stays a Host decision.
func (e *Engine) ValidateRestorableTree(
	ctx context.Context,
	rootDeployment Deployment,
	snapshot TreeSnapshot,
) error {
	if e == nil {
		return ErrInvalidEngineConfig
	}
	ctx = RequireContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	restoration, err := e.newRestoration(rootDeployment, snapshot)
	if err != nil {
		return err
	}
	return restoration.prepare(ctx)
}

func (e *Engine) newRestoration(
	rootDeployment Deployment,
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
	if !rootSnapshot.Valid() || rootSnapshot.DeploymentRef() != rootDeployment.DeploymentRef() {
		return nil, fmt.Errorf("%w: exact root Deployment does not match", ErrInvalidTreeSnapshot)
	}
	restoration := &treeRestoration{engine: e, wire: wire, deployments: make(map[DeploymentRef]Deployment)}
	if err := restoration.bind(rootDeployment); err != nil {
		return nil, err
	}
	return restoration, nil
}

func (e *Engine) startRestoredTree(ctx context.Context, restoration *treeRestoration) *Process {
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
	return &Process{handle: root}
}

func (e *Engine) reserveRestoredTree(restoration *treeRestoration) error {
	if restoration == nil || len(restoration.wire.ProcessSnapshots) == 0 {
		return ErrInvalidTreeSnapshot
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closeDone != nil {
		return ErrEngineClosed
	}
	rootID := restoration.wire.rootID()
	if e.treeRestoreReservations[rootID] != nil || e.rootRuntime(rootID) != nil {
		return ErrProcessAlreadyExists
	}
	for _, process := range restoration.wire.ProcessSnapshots {
		if e.processIdentityTaken(process.ProcessID()) {
			return ErrProcessAlreadyExists
		}
		if identity, child := process.Relation().childIdentity(); child && e.childIdentityTaken(identity) {
			return ErrInvalidChildStart
		}
	}
	e.treeRestoreReservations[rootID] = restoration
	for _, process := range restoration.wire.ProcessSnapshots {
		e.restoredProcesses[process.ProcessID()] = restoration
		if identity, child := process.Relation().childIdentity(); child {
			e.restoredChildren[identity] = restoration
		}
	}
	return nil
}

// releaseRestoredTree requires mu and retires one reservation with both indexes.
func (e *Engine) releaseRestoredTree(restoration *treeRestoration) {
	for _, process := range restoration.wire.ProcessSnapshots {
		delete(e.restoredProcesses, process.ProcessID())
		if identity, child := process.Relation().childIdentity(); child {
			delete(e.restoredChildren, identity)
		}
	}
	delete(e.treeRestoreReservations, restoration.wire.rootID())
}

func (e *Engine) discardRestoredTree(restoration *treeRestoration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if restoration != nil && e.treeRestoreReservations[restoration.wire.rootID()] == restoration {
		e.releaseRestoredTree(restoration)
	}
}

func (e *Engine) publishRestoredTree(restoration *treeRestoration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rootID := restoration.wire.rootID()
	if e.closeDone != nil || e.treeRestoreReservations[rootID] != restoration {
		panic("agent: invalid restored tree reservation")
	}
	runtime := restoration.runtime
	if runtime == nil || runtime.rootID != rootID || e.rootRuntime(rootID) != nil {
		panic("agent: invalid restored tree runtime")
	}
	for _, process := range runtime.members.all() {
		handle := process.handle
		if _, reserved := e.startReservations[handle.processID()]; reserved || e.processes[handle.processID()] != nil {
			panic("agent: restored Process reservation changed")
		}
		e.processes[handle.processID()] = handle
		if identity, child := handle.relation.childIdentity(); child {
			e.children[identity] = handle.processID()
		}
	}
	e.releaseRestoredTree(restoration)
}

// CaptureTree quiesces the tree at Strategy-safe boundaries, commits the cut,
// and returns that acknowledged snapshot. In-flight Effects settle before the
// barrier completes. After a runtime failure, read the authoritative head from
// the Host's commit store; unacknowledged runtime state is never recoverable.
func (e *Engine) CaptureTree(ctx context.Context, rootID ProcessID) (TreeSnapshot, error) {
	if e == nil {
		return TreeSnapshot{}, ErrInvalidEngineConfig
	}
	ctx = RequireContext(ctx)
	if !rootID.Valid() {
		return TreeSnapshot{}, ErrTreeNotFound
	}

	operation, err := e.acquireTreeOperation(ctx, rootID)
	if err != nil {
		return TreeSnapshot{}, err
	}
	defer operation.release()
	runtime, err := e.runtimeForTree(rootID)
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

func (e *Engine) runtimeForTree(rootID ProcessID) (*treeRuntime, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	runtime := e.rootRuntime(rootID)
	if runtime == nil {
		return nil, ErrTreeNotFound
	}
	return runtime, nil
}

// rootRuntime requires e.mu. A registered root handle owns its tree's runtime.
func (e *Engine) rootRuntime(rootID ProcessID) *treeRuntime {
	root := e.processes[rootID]
	if root == nil || !root.relation.IsRoot() {
		return nil
	}
	return root.treeRuntime()
}
