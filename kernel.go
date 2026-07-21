package ear

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Kernel -- EAR as a scheduler: a run loop that dispatches work to runtime
// instances when there is work, and sleeps until an interrupt when there is
// not.
//
//	for {
//	    if thereIsWork {
//	        runWork()             // dispatch the next ready task to its instance
//	    } else {
//	        sleepUntilInterrupt() // block until a task is submitted or a timer fires
//	    }
//	}
//
// That is the whole of it, and it is exactly a kernel's idle loop. The Kernel
// holds a process table -- named *Runtime instances, each with its own memory,
// tenant and audit trail -- and a run queue of tasks. A task names the instance
// to run on and the intent to run; Submit enqueues one and wakes the loop, the
// way a syscall raises an interrupt. Schedule makes a task recur, the way a
// timer interrupt fires on a period -- so an instance stays live for the
// recurring occurrence of work without a busy-wait: between firings the kernel
// genuinely sleeps.
//
// Dispatch runs the target instance's normal cycle, so every guarantee still
// holds: policies gate it, the tenant boundary refuses it, the trail records
// it, the session store persists it. A governance stop is not a crash -- an
// approval gate parks the task blocked, a policy violation blocked, a tenant
// refusal blocked, an error failed -- and the kernel keeps running the rest.
//
// Nothing here reasons. The Kernel only decides *when* work runs: it is the
// control plane, hardwired, while the judgment stays in the instances. That
// separation is what makes it safe for it to be the thing that never stops.
//
// Zero dependencies: the interrupt line is a buffered channel and the clock is
// time.Now (whose monotonic reading makes the arithmetic wall-clock-safe).
// Tick and Drain advance it synchronously -- the testable heartbeat; Run
// drives it until its context is cancelled.
type Kernel struct {
	// Workers bounds fleet parallelism: how many *different* instances may run
	// concurrently. At most one cycle runs per instance regardless, so each
	// instance stays the single writer of its own memory and hash-chained
	// trail. Zero or one is the serial scheduler. Negative means one worker
	// per CPU.
	Workers int

	// Dispatcher is an optional execution seam. Unset, work runs in-process
	// through the instance's own cycle. Set, each firing is handed to the
	// seam instead -- a remote executor, a job queue, a pod -- while the
	// Kernel stays the single scheduler.
	Dispatcher func(ctx context.Context, task *Task, rt *Runtime) (DispatchStatus, string)

	// HistoryLimit caps the retained dispatch history so a kernel that runs
	// for months does not grow without bound. Zero uses defaultHistoryLimit.
	HistoryLimit int

	mu         sync.Mutex
	instances  map[string]*Runtime
	queue      []*Task
	history    []Dispatch
	inFlight   map[string]bool
	idleWaits  int
	dispatched int
	nextID     int64
	wake       chan struct{}
	running    atomic.Bool
	wg         sync.WaitGroup
}

const defaultHistoryLimit = 1024

// DispatchStatus is how one task run ended.
type DispatchStatus string

const (
	// StatusRan -- the cycle completed and produced a decision.
	StatusRan DispatchStatus = "ran"
	// StatusBlocked -- governance stopped it: a violated policy, a parked
	// approval gate, or a refused tenant boundary. Not an error.
	StatusBlocked DispatchStatus = "blocked"
	// StatusFailed -- the cycle errored (or panicked). One task failing never
	// takes the kernel down.
	StatusFailed DispatchStatus = "failed"
)

// Task is one unit of scheduled work: which instance to run it on, the intent
// to run, and -- when recurring -- the period and the next time it is due.
type Task struct {
	ID       int64
	Instance string
	Intent   Intent

	// Approval carries a human's verdict for a cycle an approval-gated policy
	// previously parked, threaded straight through to Runtime.Reason.
	Approval *ApprovalVerdict

	// Claim is the tenant boundary check for scheduled work, put on the
	// dispatch context so a task submitted for the wrong org lands blocked
	// rather than touching the instance's data.
	Claim *Claim

	// Every is the recurrence period; zero means run once.
	Every time.Duration

	// due and runs are mutated only under the kernel's lock.
	due  time.Time
	runs int
}

// Recurring reports whether this task re-arms after each firing.
func (t *Task) Recurring() bool { return t.Every > 0 }

// TaskOption configures a submitted task.
type TaskOption func(*Task)

// RunEvery makes a task recur on the given period -- a timer interrupt, so an
// instance stays live for recurring work without a busy-wait.
func RunEvery(period time.Duration) TaskOption {
	return func(t *Task) { t.Every = period }
}

// StartAfter defers a task's first run by delay.
func StartAfter(delay time.Duration) TaskOption {
	return func(t *Task) { t.due = time.Now().Add(delay) }
}

// WithVerdict attaches a human approval verdict, releasing a cycle that an
// approval-gated policy previously parked.
func WithVerdict(verdict *ApprovalVerdict) TaskOption {
	return func(t *Task) { t.Approval = verdict }
}

// ActingAs attaches the caller's claim, checked against the target instance's
// tenant at dispatch time.
func ActingAs(claim Claim) TaskOption {
	return func(t *Task) { t.Claim = &claim }
}

// Dispatch is the record of one task run: what happened, and how long it took.
type Dispatch struct {
	TaskID   int64
	Instance string
	Status   DispatchStatus
	Summary  string
	Duration time.Duration
	At       time.Time
}

func (d Dispatch) String() string {
	return fmt.Sprintf("[%s] %s #%d %s (%d ms)",
		d.At.Format("15:04:05"), d.Instance, d.TaskID, d.Status, d.Duration.Milliseconds())
}

// NewKernel builds an empty kernel. The zero value works too -- every entry
// point initializes lazily -- so &Kernel{Workers: 4} is equally valid.
func NewKernel() *Kernel {
	k := &Kernel{}
	k.mu.Lock()
	k.ensure()
	k.mu.Unlock()
	return k
}

// ensure initializes the lazy fields. Callers must hold the lock.
func (k *Kernel) ensure() {
	if k.instances == nil {
		k.instances = map[string]*Runtime{}
	}
	if k.inFlight == nil {
		k.inFlight = map[string]bool{}
	}
	if k.wake == nil {
		k.wake = make(chan struct{}, 1)
	}
}

// -- the process table -------------------------------------------------------

// Register adds a runtime instance to the process table under name, replacing
// any instance already registered there.
func (k *Kernel) Register(name string, rt *Runtime) *Runtime {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ensure()
	k.instances[name] = rt
	return rt
}

// Remove drops an instance from the process table. Queued tasks naming it
// remain queued and will fail at dispatch -- removing an instance is not a way
// to cancel its work; Cancel is.
func (k *Kernel) Remove(name string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ensure()
	delete(k.instances, name)
}

// Instance returns the registered runtime under name.
func (k *Kernel) Instance(name string) (*Runtime, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ensure()
	rt, ok := k.instances[name]
	return rt, ok
}

// Instances returns the registered instance names, in no particular order.
func (k *Kernel) Instances() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ensure()
	names := make([]string, 0, len(k.instances))
	for name := range k.instances {
		names = append(names, name)
	}
	return names
}

// -- submitting work (the interrupt) -----------------------------------------

// Submit enqueues work for an instance and wakes the loop -- a syscall raising
// an interrupt. With no options the task runs once, as soon as the loop next
// turns.
func (k *Kernel) Submit(instance string, intent Intent, opts ...TaskOption) *Task {
	task := &Task{Instance: instance, Intent: intent, due: time.Now()}
	for _, opt := range opts {
		opt(task)
	}

	k.mu.Lock()
	k.ensure()
	k.nextID++
	task.ID = k.nextID
	k.queue = append(k.queue, task)
	k.mu.Unlock()

	k.interrupt()
	return task
}

// Arm schedules the standing work an instance's stack authored in memory.md's
// `## Scheduled Work` section, returning the tasks it armed. This is the join
// between the two halves of the design: the enterprise declares its recurring
// work in prose, and the kernel is what makes that declaration run.
//
// A runtime with no strategy, or a strategy with no authored schedule, arms
// nothing and reports no error -- an unscheduled stack is a normal stack.
//
// Each armed task is due one period out, as a timer should be. Options are
// applied after that default, so StartAfter(0) arms the same authored work due
// immediately -- what an operator wants from a one-shot run of the schedule.
func (k *Kernel) Arm(name string, opts ...TaskOption) ([]*Task, error) {
	rt, ok := k.Instance(name)
	if !ok {
		return nil, fmt.Errorf("no such instance %q", name)
	}
	if rt.Strategy == nil {
		return nil, nil
	}
	var armed []*Task
	for _, work := range rt.Strategy.ScheduledWork {
		armed = append(armed, k.Schedule(name, NewIntent(work.Intent, nil), work.Every, opts...))
	}
	return armed, nil
}

// Schedule enqueues recurring work: a timer interrupt firing every period, so
// an instance stays live for the recurring occurrence of a task. The first
// firing is one period out, not immediate.
func (k *Kernel) Schedule(instance string, intent Intent, period time.Duration, opts ...TaskOption) *Task {
	return k.Submit(instance, intent, append([]TaskOption{RunEvery(period), StartAfter(period)}, opts...)...)
}

// Cancel removes a queued task by id, returning whether it was there. A task
// already in flight runs to completion; cancelling only unqueues future work.
func (k *Kernel) Cancel(id int64) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	for i, task := range k.queue {
		if task.ID == id {
			k.queue = append(k.queue[:i], k.queue[i+1:]...)
			return true
		}
	}
	return false
}

// Pending is the number of queued tasks.
func (k *Kernel) Pending() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.queue)
}

// interrupt wakes a sleeping loop. The wake channel is buffered to one, so a
// burst of submissions costs one wakeup and never blocks the submitter.
func (k *Kernel) interrupt() {
	k.mu.Lock()
	wake := k.wake
	k.mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

// -- the loop, one step at a time --------------------------------------------

// Tick is one turn of the loop: if a task is ready, run it and return its
// dispatch; otherwise report idle by returning nil. Non-blocking -- the
// synchronous heartbeat the live loop is built from and tests drive directly.
func (k *Kernel) Tick(ctx context.Context) *Dispatch {
	if ctx == nil {
		ctx = context.Background()
	}
	task := k.takeReady(time.Now(), false)
	if task == nil {
		k.mu.Lock()
		k.idleWaits++
		k.mu.Unlock()
		return nil
	}
	dispatch := k.dispatch(ctx, task)
	return &dispatch
}

// Drain runs every task that is ready right now, in due order, and returns
// what ran -- the way to advance the kernel synchronously. maxUnits bounds the
// work so a self-resubmitting task cannot spin forever; zero uses a sane cap.
func (k *Kernel) Drain(ctx context.Context, maxUnits int) []Dispatch {
	if maxUnits <= 0 {
		maxUnits = 10_000
	}
	if k.workers() > 1 {
		return k.drainConcurrent(ctx, maxUnits)
	}
	var done []Dispatch
	for len(done) < maxUnits {
		dispatch := k.Tick(ctx)
		if dispatch == nil {
			break
		}
		done = append(done, *dispatch)
	}
	return done
}

// takeReady returns the soonest-due task whose time has come, removed from the
// queue (or re-armed, if recurring). When reserve is set it also skips
// instances already in flight and reserves the one it takes -- the atomic
// take-and-reserve that keeps at most one cycle running per instance.
func (k *Kernel) takeReady(now time.Time, reserve bool) *Task {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ensure()

	best, bestIndex := (*Task)(nil), -1
	for i, task := range k.queue {
		if task.due.After(now) {
			continue
		}
		if reserve && k.inFlight[task.Instance] {
			continue
		}
		if best == nil || task.due.Before(best.due) {
			best, bestIndex = task, i
		}
	}
	if best == nil {
		return nil
	}

	if best.Recurring() {
		best.due = now.Add(best.Every)
	} else {
		k.queue = append(k.queue[:bestIndex], k.queue[bestIndex+1:]...)
	}
	if reserve {
		k.inFlight[best.Instance] = true
	}
	return best
}

// -- fleet parallelism: many instances at once, one cycle per instance -------

// workers resolves the configured pool size. Negative means one per CPU.
func (k *Kernel) workers() int {
	switch {
	case k.Workers < 0:
		return runtime.GOMAXPROCS(0)
	case k.Workers < 1:
		return 1
	default:
		return k.Workers
	}
}

// drainConcurrent runs every ready task, fanning out across *different*
// instances while serializing work *within* an instance.
func (k *Kernel) drainConcurrent(ctx context.Context, maxUnits int) []Dispatch {
	limit := k.workers()
	sem := make(chan struct{}, limit)

	var mu sync.Mutex
	var done []Dispatch

	for {
		var wg sync.WaitGroup
		launched := 0
		for {
			mu.Lock()
			room := len(done)+launched < maxUnits
			mu.Unlock()
			if !room {
				break
			}
			task := k.takeReady(time.Now(), true)
			if task == nil {
				break
			}
			launched++
			wg.Add(1)
			sem <- struct{}{}
			go func(task *Task) {
				defer wg.Done()
				defer func() { <-sem }()
				dispatch := k.dispatchAndRelease(ctx, task)
				mu.Lock()
				done = append(done, dispatch)
				mu.Unlock()
			}(task)
		}
		wg.Wait()
		if launched == 0 {
			break
		}
	}
	return done
}

// submitReady launches every ready, free-instance task without blocking,
// returning how many it launched. The non-blocking step the parallel idle loop
// is built from.
func (k *Kernel) submitReady(ctx context.Context, sem chan struct{}) int {
	launched := 0
	for {
		task := k.takeReady(time.Now(), true)
		if task == nil {
			return launched
		}
		k.wg.Add(1)
		sem <- struct{}{}
		go func(task *Task) {
			defer k.wg.Done()
			defer func() { <-sem }()
			k.dispatchAndRelease(ctx, task)
		}(task)
		launched++
	}
}

func (k *Kernel) dispatchAndRelease(ctx context.Context, task *Task) Dispatch {
	defer func() {
		k.mu.Lock()
		delete(k.inFlight, task.Instance)
		k.mu.Unlock()
	}()
	return k.dispatch(ctx, task)
}

// busy reports whether any instance is mid-cycle.
func (k *Kernel) busy() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.inFlight) > 0
}

// -- run_work(): one task, on its instance, through the normal cycle ---------

// dispatch runs the task on its instance. A governance stop parks it blocked,
// an error or a panic fails it -- neither takes the kernel down.
func (k *Kernel) dispatch(ctx context.Context, task *Task) Dispatch {
	started := time.Now()

	k.mu.Lock()
	k.ensure()
	rt := k.instances[task.Instance]
	task.runs++
	k.mu.Unlock()

	var status DispatchStatus
	var summary string
	switch {
	case rt == nil:
		status, summary = StatusFailed, fmt.Sprintf("no such instance %q", task.Instance)
	default:
		status, summary = k.runTask(ctx, task, rt)
	}

	dispatch := Dispatch{
		TaskID:   task.ID,
		Instance: task.Instance,
		Status:   status,
		Summary:  truncate(summary, 240),
		Duration: time.Since(started),
		At:       started,
	}
	k.record(dispatch)
	return dispatch
}

// runTask performs the cycle itself, converting every outcome -- including a
// panic in a seam or a bound tool -- into a status. A panic in a dispatch
// goroutine would otherwise take the whole process down, which for a control
// plane that is meant never to stop is the one unacceptable failure mode.
func (k *Kernel) runTask(ctx context.Context, task *Task, rt *Runtime) (status DispatchStatus, summary string) {
	defer func() {
		if r := recover(); r != nil {
			status, summary = StatusFailed, fmt.Sprintf("panic: %v", r)
		}
	}()

	// The claim rides the context, so the tenant boundary is enforced by the
	// runtime itself -- scheduled work gets exactly the check a direct call
	// gets, through the same code path.
	if task.Claim != nil {
		ctx = WithClaim(ctx, *task.Claim)
	}

	if k.Dispatcher != nil {
		return k.Dispatcher(ctx, task, rt)
	}

	decision, err := rt.Reason(ctx, task.Intent, task.Approval)
	if err != nil {
		return classifyDispatch(err)
	}
	return StatusRan, fmt.Sprint(decision)
}

// classifyDispatch separates governance from failure. A refusal EAR is
// designed to produce -- a violated policy, a parked gate, a tenant boundary,
// a denied spawn -- is the system working, and lands blocked. Anything else
// is a fault, and lands failed.
func classifyDispatch(err error) (DispatchStatus, string) {
	var (
		policy   *PolicyViolationError
		approval *ApprovalRequiredError
		tenant   *TenantBoundaryError
		spawn    *SpawnDeniedError
	)
	switch {
	case errors.As(err, &approval):
		return StatusBlocked, "awaiting approval: " + approval.Error()
	case errors.As(err, &policy):
		return StatusBlocked, policy.Error()
	case errors.As(err, &tenant):
		return StatusBlocked, tenant.Error()
	case errors.As(err, &spawn):
		return StatusBlocked, spawn.Error()
	default:
		return StatusFailed, err.Error()
	}
}

// record appends a dispatch to the history, trimming to the retention cap.
func (k *Kernel) record(dispatch Dispatch) {
	k.mu.Lock()
	defer k.mu.Unlock()
	limit := k.HistoryLimit
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	k.history = append(k.history, dispatch)
	k.dispatched++
	if excess := len(k.history) - limit; excess > 0 {
		k.history = append(k.history[:0], k.history[excess:]...)
	}
}

// History returns a copy of the retained dispatch history, oldest first.
func (k *Kernel) History() []Dispatch {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]Dispatch{}, k.history...)
}

// -- the blocking idle loop --------------------------------------------------

// Run drives the loop until ctx is cancelled: while there is work, run it;
// otherwise sleep until an interrupt. It returns ctx.Err() after every
// in-flight cycle has finished, so a cancelled kernel never abandons a runtime
// mid-write to its memory or trail.
//
// Run is the persistent form of the runtime. Call it in a goroutine and cancel
// the context to stop:
//
//	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
//	defer stop()
//	kernel.Run(ctx)
func (k *Kernel) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !k.running.CompareAndSwap(false, true) {
		return errors.New("kernel is already running")
	}
	defer k.running.Store(false)

	parallel := k.workers() > 1
	sem := make(chan struct{}, k.workers())

	for {
		if err := ctx.Err(); err != nil {
			k.wg.Wait() // let in-flight cycles finish writing
			return err
		}
		if parallel {
			if k.submitReady(ctx, sem) == 0 && !k.busy() {
				k.sleepUntilInterrupt(ctx)
			}
		} else if k.Tick(ctx) == nil {
			k.sleepUntilInterrupt(ctx)
		}
	}
}

// Running reports whether a Run loop is currently driving this kernel.
func (k *Kernel) Running() bool { return k.running.Load() }

// sleepUntilInterrupt blocks until a task is submitted, the next timer is due,
// or the context is cancelled. No busy-wait: the CPU is genuinely idle here
// until something actually needs doing.
func (k *Kernel) sleepUntilInterrupt(ctx context.Context) {
	k.mu.Lock()
	k.ensure()
	wake := k.wake
	k.mu.Unlock()

	delay, scheduled := k.untilNext(time.Now())
	if !scheduled {
		select {
		case <-ctx.Done():
		case <-wake:
		}
		return
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-wake:
	case <-timer.C:
	}
}

// untilNext is how long until the next scheduled task is due -- the timeout
// the idle sleep waits for before the next timer interrupt. The second result
// is false when nothing is scheduled, meaning sleep indefinitely.
func (k *Kernel) untilNext(now time.Time) (time.Duration, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	var soonest time.Time
	for _, task := range k.queue {
		if task.due.After(now) && (soonest.IsZero() || task.due.Before(soonest)) {
			soonest = task.due
		}
	}
	if soonest.IsZero() {
		return 0, false
	}
	return soonest.Sub(now), true
}

// -- the process table, for a control room -----------------------------------

// Snapshot is a glance at the scheduler: the process table, the queue depth,
// and how the last dispatches went.
type Snapshot struct {
	Instances  []string
	Pending    int
	InFlight   []string
	Running    bool
	IdleWaits  int
	Dispatched int
	Recent     []Dispatch
}

// Snapshot takes a consistent read of the kernel for a monitor or a status
// command.
func (k *Kernel) Snapshot() Snapshot {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ensure()

	snap := Snapshot{
		Pending:    len(k.queue),
		Running:    k.running.Load(),
		IdleWaits:  k.idleWaits,
		Dispatched: k.dispatched,
	}
	for name := range k.instances {
		snap.Instances = append(snap.Instances, name)
	}
	for name := range k.inFlight {
		snap.InFlight = append(snap.InFlight, name)
	}
	if n := len(k.history); n > 0 {
		from := n - 8
		if from < 0 {
			from = 0
		}
		snap.Recent = append([]Dispatch{}, k.history[from:]...)
	}
	return snap
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}
