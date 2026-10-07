// Package jobs is the process-local, in-memory store of background jobs
// (X-16, P8 spec §2): a pr_review or pr_ask run that keeps going after the
// MCP call that started it has answered with a job id.
//
// The store knows nothing about MCP. A job's result is an opaque value of
// the type parameter R, kept as the run returned it, so the tool layer gets
// back exactly the result it would have returned synchronously. Nothing is
// written to disk and nothing is logged here.
//
// Limits:
//   - at most MaxRunning jobs run at once; Start refuses one more with
//     ErrTooMany;
//   - at most MaxKept finished jobs are kept; when one more finishes, the
//     job that finished first is evicted. The newest result is the one a
//     client is most likely still polling for, and the oldest is the one
//     closest to its own expiry, so this loses the least. Refusing to keep
//     the new result instead would throw away a run that already did its
//     work (and may have published). A running job is never evicted;
//   - a finished job expires TTL after it finished. Expiry is lazy: every
//     Start, Get and Wait first drops the expired jobs, so no goroutine
//     outlives Close.
//
// Credentials (X-10): a job never stores its run function. The run closure,
// and the per-call config it captures, live only on the stack of the
// goroutine that executes it, so they become garbage once the run returns.
// A finished job holds only its result or its failure message.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"strings"
	"sync"
	"time"
)

// Store limits (P8 spec §2.1).
const (
	// MaxRunning is the number of jobs that may run at once.
	MaxRunning = 4
	// MaxKept is the number of finished jobs kept for Get and Wait.
	MaxKept = 64
	// TTL is how long a finished job is kept after it finished.
	TTL = 30 * time.Minute
)

// Fixed client-facing sentences (X-6).
const (
	TooManySentence = "too many background jobs are running; wait for one to finish"
	UnknownSentence = "unknown or expired job_id"
	ClosedSentence  = "review-mcp is shutting down; no new background job can start"
)

// Errors of the store. Their texts are the fixed sentences above, so the
// tool layer may return them to the client as they are.
var (
	ErrTooMany = errors.New(TooManySentence)
	ErrUnknown = errors.New(UnknownSentence)
	ErrClosed  = errors.New(ClosedSentence)
)

// IDPrefix starts every job id.
const IDPrefix = "job_"

// idEncoding renders the 128 random bits of an id: the RFC 4648 base32
// alphabet, lower-cased, without padding. Padding carries no information for
// a fixed-length value and its "=" needs quoting in some clients; lower case
// is easier to read back and type, and lower-casing loses nothing because
// the alphabet has one letter case only. The result is "job_" plus 26
// characters of [a-z2-7].
var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// State is the state of a job.
type State string

// Job states.
const (
	StateRunning State = "running"
	StateDone    State = "done"
	StateFailed  State = "failed"
)

// RunFunc is the work of one job. It runs on its own goroutine with the
// job's context, which ends only when the store is closed, and reports its
// progress stages through progress (safe to call from any goroutine; a call
// after the run returned is ignored).
//
// A non-nil error fails the job, and the error's text becomes the job's
// failure message as it is: the run must return a classified error whose
// text is a fixed client sentence (X-6), never raw upstream text.
type RunFunc[R any] func(ctx context.Context, progress func(stage string)) (R, error)

// Snapshot is a copy of a job's state at one moment.
type Snapshot[R any] struct {
	ID string
	// Tag is the label the job was started with (the tool name).
	Tag   string
	State State
	// Stage is the last progress stage the run reported ("" before the
	// first one).
	Stage string
	// Elapsed is the time since the job started, or its whole run time
	// once it finished.
	Elapsed time.Duration
	// Result is the run's result (StateDone only).
	Result R
	// Message is the classified failure message (StateFailed only).
	Message string
}

// ElapsedSeconds is Elapsed in whole seconds, rounded down.
func (s Snapshot[R]) ElapsedSeconds() int {
	return int(s.Elapsed / time.Second)
}

// Options configure a Store.
type Options struct {
	// Now is the clock of the TTL and the elapsed times (nil: time.Now).
	// Waiting always uses real time.
	Now func() time.Time
}

// Store is the job store. It is safe for concurrent use.
type Store[R any] struct {
	now    func() time.Time
	base   context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	closed  bool
	running int
	jobs    map[string]*job[R]
}

type job[R any] struct {
	id       string
	tag      string
	started  time.Time
	finished time.Time
	state    State
	// stages are the distinct stages the run reported, in order; the
	// last one is the current stage. A run has a handful of them.
	stages  []string
	result  R
	message string
	// changed is closed and replaced on every stage change, and closed for
	// good when the job finishes.
	changed chan struct{}
}

// New returns an empty store. Close it at shutdown.
func New[R any](opts Options) *Store[R] {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	base, cancel := context.WithCancel(context.Background()) //nolint:gosec // G118: Close calls cancel at shutdown
	return &Store[R]{now: now, base: base, cancel: cancel, jobs: map[string]*job[R]{}}
}

// Close cancels the context of every running job and makes Start fail with
// ErrClosed. It does not wait for the runs to return: a job never keeps the
// process alive. Finished jobs stay readable. Close is idempotent.
func (s *Store[R]) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
}

// Start starts run as a new job and returns its id. tag is an opaque label
// kept with the job and reported in its snapshots (the tool layer stores the
// tool name). The job's context keeps
// the values of ctx (the caller's request context) but not its cancellation
// or deadline: it ends only when the store is closed. It fails with
// ErrTooMany when MaxRunning jobs are running, and with ErrClosed after
// Close.
func (s *Store[R]) Start(ctx context.Context, tag string, run RunFunc[R]) (string, error) {
	s.mu.Lock()
	s.sweepLocked()
	if s.closed {
		s.mu.Unlock()
		return "", ErrClosed
	}
	if s.running >= MaxRunning {
		s.mu.Unlock()
		return "", ErrTooMany
	}
	id := s.newIDLocked()
	j := &job[R]{
		id:      id,
		tag:     tag,
		started: s.now(),
		state:   StateRunning,
		changed: make(chan struct{}),
	}
	s.jobs[id] = j
	s.running++
	s.mu.Unlock()

	jctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(s.base, cancel)
	go func() {
		defer cancel()
		defer stop()
		result, err := run(jctx, func(stage string) { s.setStage(j, stage) })
		s.finish(j, result, err)
	}()
	return id, nil
}

// Get returns a snapshot of the job, or ErrUnknown when there is no job
// with that id (never started, expired or evicted).
func (s *Store[R]) Get(id string) (Snapshot[R], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	j, ok := s.jobs[id]
	if !ok {
		return Snapshot[R]{}, ErrUnknown
	}
	return s.snapshotLocked(j), nil
}

// Wait waits until the job finishes, d elapses or ctx ends, whichever comes
// first, and returns a snapshot of the job then. A d of zero or less does
// not wait. When ctx ends first, the snapshot comes with ctx's error.
func (s *Store[R]) Wait(ctx context.Context, id string, d time.Duration) (Snapshot[R], error) {
	return s.WaitProgress(ctx, id, d, nil)
}

// WaitProgress is Wait that also calls onStage (when not nil) with every
// stage the job has reached, in order: first the stages reached before the
// wait started, then each new one as it is reported. It runs on the waiting
// goroutine, never with the store locked, and only until WaitProgress
// returns, so a caller that sends progress notifications sends them only
// while its own request is open. No stage is lost to a fast run, and a stage
// set twice in a row is reported once.
func (s *Store[R]) WaitProgress(ctx context.Context, id string, d time.Duration, onStage func(stage string)) (Snapshot[R], error) {
	s.mu.Lock()
	s.sweepLocked()
	j, ok := s.jobs[id]
	s.mu.Unlock()
	if !ok {
		return Snapshot[R]{}, ErrUnknown
	}

	var timeout <-chan time.Time
	if d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		timeout = t.C
	}
	reported := 0
	var ctxErr error
	timedOut := false
	for {
		s.mu.Lock()
		snap := s.snapshotLocked(j)
		changed := j.changed
		fresh := append([]string(nil), j.stages[reported:]...)
		s.mu.Unlock()
		reported += len(fresh)
		if onStage != nil {
			for _, stage := range fresh {
				onStage(stage)
			}
		}
		// Every way out goes through the snapshot above, so the stages that
		// came with the finish (or just before the timeout) are reported.
		if ctxErr != nil {
			return snap, ctxErr
		}
		if snap.State != StateRunning || timeout == nil || timedOut {
			return snap, nil
		}
		select {
		case <-changed: // also closed when the job finishes
		case <-timeout:
			timedOut = true
		case <-ctx.Done():
			ctxErr = ctx.Err()
		}
	}
}

func (s *Store[R]) setStage(j *job[R], stage string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j.state != StateRunning || (len(j.stages) > 0 && j.stages[len(j.stages)-1] == stage) {
		return
	}
	j.stages = append(j.stages, stage)
	close(j.changed)
	j.changed = make(chan struct{})
}

func (s *Store[R]) finish(j *job[R], result R, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j.finished = s.now()
	if err != nil {
		j.state = StateFailed
		j.message = err.Error()
	} else {
		j.state = StateDone
		j.result = result
	}
	s.running--
	close(j.changed)
	s.sweepLocked()
	s.evictLocked()
}

func (s *Store[R]) snapshotLocked(j *job[R]) Snapshot[R] {
	snap := Snapshot[R]{ID: j.id, Tag: j.tag, State: j.state, Result: j.result, Message: j.message}
	if len(j.stages) > 0 {
		snap.Stage = j.stages[len(j.stages)-1]
	}
	if j.state == StateRunning {
		snap.Elapsed = s.now().Sub(j.started)
	} else {
		snap.Elapsed = j.finished.Sub(j.started)
	}
	if snap.Elapsed < 0 {
		snap.Elapsed = 0
	}
	return snap
}

// sweepLocked drops the finished jobs whose TTL has run out.
func (s *Store[R]) sweepLocked() {
	now := s.now()
	for id, j := range s.jobs {
		if j.state != StateRunning && !now.Before(j.finished.Add(TTL)) {
			delete(s.jobs, id)
		}
	}
}

// evictLocked drops the finished jobs that finished first until at most
// MaxKept are left. Running jobs are never dropped.
func (s *Store[R]) evictLocked() {
	for {
		var oldest *job[R]
		kept := 0
		for _, j := range s.jobs {
			if j.state == StateRunning {
				continue
			}
			kept++
			if oldest == nil || j.finished.Before(oldest.finished) {
				oldest = j
			}
		}
		if kept <= MaxKept {
			return
		}
		delete(s.jobs, oldest.id)
	}
}

// newIDLocked returns a fresh job id: 128 bits from crypto/rand. A
// collision with a known id is astronomically unlikely; it is retried
// anyway, since checking costs nothing.
func (s *Store[R]) newIDLocked() string {
	for {
		var b [16]byte
		// crypto/rand.Read never returns an error (it crashes the program
		// instead, since Go 1.24).
		_, _ = rand.Read(b[:])
		id := IDPrefix + strings.ToLower(idEncoding.EncodeToString(b[:]))
		if _, taken := s.jobs[id]; !taken {
			return id
		}
	}
}
