package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
)

// EngineConfig keeps scheduling and authority policy outside Deployments so a
// strategy cannot change Engine-wide constraints through its behavior binding.
type EngineConfig struct {
	// A nil port permits ephemeral execution without requiring storage. A port
	// makes publication wait for an acknowledged, recoverable tree.
	TreeDurability TreeDurability

	ProcessStartOutcomeAcknowledger ProcessStartOutcomeAcknowledger

	// Exact local bindings prevent restoration from silently selecting different
	// behavior. Same-Deployment recursion needs no resolver.
	DeploymentResolver DeploymentResolver

	ProcessAdmitter ProcessAdmitter

	EventListeners []EventListener

	DeltaListeners []DeltaListener

	// A bounded queue prevents slow listeners from retaining unlimited Deltas.
	// Zero selects the library default; negative capacities are invalid.
	DeltaBufferCapacity int

	// Zero fields inherit DefaultLimits so partial overrides still produce
	// complete per-Process resource bounds.
	Limits Limits

	TreeLimits TreeLimits

	// Children receive only subsets of root authority so composition cannot
	// escalate privileges through a child Effect.
	Capabilities CapabilitySet
}

// Engine keeps admission, publication, and execution under one owner because
// resource reservations and recoverable tree state must describe the same
// lifecycle. Construct it with NewEngine; copying an Engine would share its
// registries while duplicating their synchronization.
type Engine struct {
	durability               TreeDurability
	startOutcomeAcknowledger ProcessStartOutcomeAcknowledger
	resolver                 DeploymentResolver
	admitter                 ProcessAdmitter
	observation              *observationBus
	limits                   Limits
	treeLimits               TreeLimits
	capabilities             CapabilitySet

	// Tree-wide administrative operations use an independent serial lane so a
	// caller waiting for one root never holds the registry lock needed by Engine
	// lifecycle paths. The two locks are therefore never nested.
	treeOperationsMu sync.Mutex
	treeOperations   map[ProcessID]*treeOperation

	// Registry and reservation changes share this lock so publication cannot
	// expose a Process whose admission still appears unreserved.
	mu                      sync.RWMutex
	processes               map[ProcessID]*processHandleState
	trees                   map[ProcessID]*treeRuntime
	startReservations       map[ProcessID]processStartReservation
	treeRestoreReservations map[ProcessID]*treeRestoration
	children                map[childIdentity]ProcessID
	childStartReservations  map[childIdentity]ProcessID
	// One channel closes admission at allocation and joins completion on close,
	// avoiding separate shutdown flags that could disagree.
	closeDone chan struct{}
}

func (e *Engine) ObservationFailures() ObservationFailureCounts {
	if e == nil || e.observation == nil {
		return ObservationFailureCounts{}
	}
	return e.observation.failureCounts()
}

// FlushDeltas provides the ordering barrier needed before publishing a final
// value that must not overtake accepted streaming observations. Dropped Deltas
// remain lost because flushing cannot strengthen best-effort delivery.
func (e *Engine) FlushDeltas(ctx context.Context) error {
	if e == nil {
		return ErrEngineClosed
	}
	return e.observation.flushDeltas(ctx)
}

// NewEngine validates the whole configuration up front because an Engine owns
// Process lifecycle: a defect discovered after Processes exist has no safe
// remedy, since stopping the Engine would abandon in-flight effects whose
// settlement is still unknown.
func NewEngine(config EngineConfig) (*Engine, error) {
	if config.DeltaBufferCapacity < 0 {
		return nil, fmt.Errorf("%w: DeltaBufferCapacity must not be negative", ErrInvalidEngineConfig)
	}
	if config.TreeDurability != nil && lo.IsNil(config.TreeDurability) {
		return nil, fmt.Errorf("%w: TreeDurability is typed nil", ErrInvalidEngineConfig)
	}
	if config.ProcessStartOutcomeAcknowledger != nil && lo.IsNil(config.ProcessStartOutcomeAcknowledger) {
		return nil, fmt.Errorf("%w: ProcessStartOutcomeAcknowledger is typed nil", ErrInvalidEngineConfig)
	}
	if config.DeploymentResolver != nil && lo.IsNil(config.DeploymentResolver) {
		return nil, fmt.Errorf("%w: DeploymentResolver is typed nil", ErrInvalidEngineConfig)
	}
	if config.ProcessAdmitter != nil && lo.IsNil(config.ProcessAdmitter) {
		return nil, fmt.Errorf("%w: ProcessAdmitter is typed nil", ErrInvalidEngineConfig)
	}
	for index, listener := range config.EventListeners {
		if lo.IsNil(listener) {
			return nil, fmt.Errorf("%w: EventListeners[%d] is nil", ErrInvalidEngineConfig, index)
		}
	}
	for index, listener := range config.DeltaListeners {
		if lo.IsNil(listener) {
			return nil, fmt.Errorf("%w: DeltaListeners[%d] is nil", ErrInvalidEngineConfig, index)
		}
	}
	capacity := config.DeltaBufferCapacity
	if capacity == 0 {
		capacity = defaultDeltaBuffer
	}
	limits, err := config.Limits.resolve()
	if err != nil {
		return nil, err
	}
	treeLimits, err := config.TreeLimits.resolve()
	if err != nil {
		return nil, err
	}
	if !config.Capabilities.Valid() {
		return nil, fmt.Errorf("%w: capabilities are invalid", ErrInvalidEngineConfig)
	}
	return &Engine{
		durability:               config.TreeDurability,
		startOutcomeAcknowledger: config.ProcessStartOutcomeAcknowledger,
		resolver:                 config.DeploymentResolver,
		admitter:                 config.ProcessAdmitter,
		observation:              newObservationBus(config.EventListeners, config.DeltaListeners, capacity),
		limits:                   limits,
		treeLimits:               treeLimits,
		capabilities:             config.Capabilities,
		treeOperations:           make(map[ProcessID]*treeOperation),
		processes:                make(map[ProcessID]*processHandleState),
		trees:                    make(map[ProcessID]*treeRuntime),
		startReservations:        make(map[ProcessID]processStartReservation),
		treeRestoreReservations:  make(map[ProcessID]*treeRestoration),
		children:                 make(map[childIdentity]ProcessID),
		childStartReservations:   make(map[childIdentity]ProcessID),
	}, nil
}

type childIdentity struct {
	parent ProcessID
	key    ChildKey
}

// Start keeps ctx attached to the resulting tree so Host cancellation and
// deadlines reach accepted work. Execution that outlives a request therefore
// needs a longer-lived context.
func (e *Engine) Start(ctx context.Context, deployment Deployment, input Input) (*Process, error) {
	if e == nil {
		return nil, ErrInvalidEngineConfig
	}
	ctx = requireContext(ctx)
	if !deployment.Valid() {
		return nil, ErrInvalidDeployment
	}
	if err := deployment.Descriptor().ValidateInput(input); err != nil {
		return nil, err
	}
	id, err := newProcessID()
	if err != nil {
		return nil, err
	}
	relation := rootProcessRelation(id)
	budget := budgetFromLimits(e.limits)
	admission := newProcessAdmission(relation, deployment, budget, e.capabilities)
	if reserveProcessStartErr := e.reserveProcessStart(
		relation, deployment.DeploymentRef(), e.treeLimits, Digest{},
	); reserveProcessStartErr != nil {
		return nil, reserveProcessStartErr
	}
	if requestProcessAdmissionErr := requestProcessAdmission(ctx, e.admitter, admission); requestProcessAdmissionErr != nil {
		e.discardProcessStartReservation(id)
		return nil, requestProcessAdmissionErr
	}
	startedAt := time.Now().Round(0).UTC()
	execution, state, failure, err := initializeExecution(deployment.Definition(), input)
	if err != nil {
		acknowledgeErr := acknowledgeProcessStartOutcome(ctx, e.startOutcomeAcknowledger, abortedProcessOutcome(admission, failure))
		e.discardProcessStartReservation(id)
		return nil, errors.Join(fmt.Errorf("agent: initialize Process: %w", err), acknowledgeErr)
	}
	if err := acknowledgeProcessStartOutcome(ctx, e.startOutcomeAcknowledger, startedProcessOutcome(admission, startedAt)); err != nil {
		e.discardProcessStartReservation(id)
		return nil, err
	}
	handle := newProcessHandleState(
		relation, deployment.DeploymentRef(), budget, e.capabilities,
		e.treeLimits,
		startedAt, StatusRunning,
	)
	process := newProcessState(e, handle, deployment, execution, state, startedAt, e.limits)
	runtime := newTreeRuntime(e, relation.RootID(), ctx, process)
	if e.durability != nil {
		incarnation, incarnationErr := newTreeIncarnationID()
		if incarnationErr != nil {
			e.discardProcessStartReservation(id)
			return nil, incarnationErr
		}
		runtime.incarnation = incarnation
		baseSnapshot, captureErr := runtime.captureTree()
		if captureErr != nil {
			e.discardProcessStartReservation(id)
			return nil, captureErr
		}
		checkpoint, err := newTreeCheckpoint(TreeCheckpointStart, Digest{}, baseSnapshot)
		if err != nil {
			e.discardProcessStartReservation(id)
			return nil, err
		}
		if err := commitTreeCheckpoint(ctx, e.durability, checkpoint); err != nil {
			e.discardProcessStartReservation(id)
			return nil, err
		}
		runtime.establishDurableHead(incarnation, baseSnapshot)
	}
	e.publishReservedProcess(handle)
	go runtime.run(ctx)
	return &Process{handle: handle}, nil
}

// Run keeps waiting after cancellation because accepted Effects must settle
// before the terminal result can describe their outcomes truthfully.
func (e *Engine) Run(ctx context.Context, deployment Deployment, input Input) (Result, error) {
	process, err := e.Start(ctx, deployment, input)
	if err != nil {
		return Result{}, err
	}
	return process.Await(context.WithoutCancel(requireContext(ctx)))
}

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

// Close rejects active Processes, pending starts or restorations, and trees
// that still own asynchronous work or a freeze. Once closing begins, it joins
// remaining outcome bookkeeping before stopping observation workers.
// Concurrent callers join the same closure; existing handles retain results
// and RuntimeErrors for later reads.
func (e *Engine) Close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	if e.closeDone != nil {
		done := e.closeDone
		e.mu.Unlock()
		<-done
		return nil
	}
	if len(e.startReservations) != 0 || len(e.treeRestoreReservations) != 0 {
		e.mu.Unlock()
		return fmt.Errorf("%w: Process publication is pending", ErrEngineHasActiveProcesses)
	}
	var pendingBookkeeping []*processHandleState
	for _, handle := range e.processes {
		select {
		case <-handle.outcomePublished:
		default:
			if !handle.status().Terminal() {
				e.mu.Unlock()
				return fmt.Errorf(
					"%w: Process %s is still running",
					ErrEngineHasActiveProcesses, handle.processID,
				)
			}
		}
		select {
		case <-handle.bookkeepingDone:
		default:
			pendingBookkeeping = append(pendingBookkeeping, handle)
		}
	}
	for rootID, runtime := range e.trees {
		if runtime.inFlightWork.Load() != 0 || runtime.freezeActive.Load() {
			e.mu.Unlock()
			return fmt.Errorf(
				"%w: tree %s still owns active work",
				ErrEngineHasActiveProcesses, rootID,
			)
		}
	}
	if len(pendingBookkeeping) != 0 && e.durability != nil {
		e.mu.Unlock()
		return fmt.Errorf(
			"%w: Process %s has an unpublished outcome or pending parent/child bookkeeping",
			ErrEngineHasActiveProcesses, pendingBookkeeping[0].processID,
		)
	}
	done := make(chan struct{})
	e.closeDone = done
	e.mu.Unlock()
	// Close admission before joining publication and parent/child bookkeeping,
	// whose listeners may inspect the registry and therefore need e.mu.
	for _, handle := range pendingBookkeeping {
		<-handle.bookkeepingDone
	}
	e.observation.close()
	close(done)
	return nil
}

func newProcessID() (ProcessID, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return ProcessID{}, fmt.Errorf("agent: generate ProcessID: %w", err)
	}
	return ParseProcessID(processIDPrefix + hex.EncodeToString(random[:]))
}
