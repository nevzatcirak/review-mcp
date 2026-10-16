package jobs

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"weak"
)

// fakeClock is the injectable clock of the TTL tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// result stands in for a tool result: text plus structured content.
type result struct {
	Text       string
	Structured []byte
}

// blocker is a run that blocks until released (or its context ends).
type blocker struct {
	release chan struct{}
	started chan struct{}
}

func newBlocker() *blocker {
	return &blocker{release: make(chan struct{}), started: make(chan struct{})}
}

func (b *blocker) run(res *result) RunFunc[*result] {
	return func(ctx context.Context, _ func(string)) (*result, error) {
		close(b.started)
		select {
		case <-b.release:
			return res, nil
		case <-ctx.Done():
			return nil, errors.New("the run was cancelled")
		}
	}
}

func instant(res *result) RunFunc[*result] {
	return func(context.Context, func(string)) (*result, error) { return res, nil }
}

// waitFinished waits (in real time) for a job to finish.
func waitFinished(t *testing.T, s *Store[*result], id string) Snapshot[*result] {
	t.Helper()
	snap, err := s.Wait(context.Background(), id, 5*time.Second)
	if err != nil {
		t.Fatalf("Wait(%s): %v", id, err)
	}
	if snap.State == StateRunning || snap.State == StateQueued {
		t.Fatalf("job %s still %s after 5 s", id, snap.State)
	}
	return snap
}

func TestIDFormatAndUniqueness(t *testing.T) {
	s := New[*result](Options{})
	defer s.Close()
	re := regexp.MustCompile(`^job_[a-z2-7]{26}$`)
	seen := map[string]bool{}
	for i := range 1000 {
		s.mu.Lock()
		id := s.newIDLocked()
		s.mu.Unlock()
		if !re.MatchString(id) {
			t.Fatalf("id %d = %q, want job_ plus 26 lower-case base32 characters", i, id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}

	id, err := s.Start(context.Background(), "test", instant(&result{}))
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString(id) {
		t.Errorf("Start id = %q", id)
	}
}

// hasStarted reports whether the blocker's run has begun, giving a run that
// should have started a moment to do so.
func hasStarted(b *blocker, wait time.Duration) bool {
	select {
	case <-b.started:
		return true
	case <-time.After(wait):
		return false
	}
}

// fillRunning starts MaxRunning blockers and waits until all of them run.
func fillRunning(t *testing.T, s *Store[*result]) ([]*blocker, []string) {
	t.Helper()
	var blockers []*blocker
	var ids []string
	for i := range MaxRunning {
		b := newBlocker()
		id, err := s.Start(context.Background(), "test", b.run(&result{Text: fmt.Sprint(i)}))
		if err != nil {
			t.Fatalf("job %d: %v", i+1, err)
		}
		if !hasStarted(b, 5*time.Second) {
			t.Fatalf("job %d did not start", i+1)
		}
		blockers = append(blockers, b)
		ids = append(ids, id)
	}
	return blockers, ids
}

// TestFifthJobIsQueuedUntilOneFinishes [canary target]: with MaxRunning jobs
// running, one more is queued at position 1 instead of refused, and starts
// when one finishes.
func TestFifthJobIsQueuedUntilOneFinishes(t *testing.T) {
	s := New[*result](Options{})
	defer s.Close()
	blockers, ids := fillRunning(t, s)

	fifth := newBlocker()
	id, err := s.Start(context.Background(), "test", fifth.run(&result{Text: "fifth"}))
	if err != nil {
		t.Fatalf("5th Start error = %v, want it queued", err)
	}
	snap, err := s.Get(id)
	if err != nil || snap.State != StateQueued || snap.Position != 1 {
		t.Fatalf("5th job = %+v, %v; want queued at position 1", snap, err)
	}
	if hasStarted(fifth, 50*time.Millisecond) {
		t.Fatal("the 5th job started while four were running")
	}

	close(blockers[0].release)
	waitFinished(t, s, ids[0])
	if !hasStarted(fifth, 5*time.Second) {
		t.Fatal("the queued job did not start when a running one finished")
	}
	if snap, _ := s.Get(id); snap.State != StateRunning || snap.Position != 0 {
		t.Errorf("started job = %+v, want running at position 0", snap)
	}
	close(fifth.release)
	if snap := waitFinished(t, s, id); snap.State != StateDone || snap.Result.Text != "fifth" {
		t.Errorf("queued job finished as %+v", snap)
	}
	for _, b := range blockers[1:] {
		close(b.release)
	}
}

// TestQueuedJobsStartInArrivalOrder [canary target]: three queued jobs start
// first come, first served, one per finished running job, and the positions
// move up.
func TestQueuedJobsStartInArrivalOrder(t *testing.T) {
	s := New[*result](Options{})
	defer s.Close()
	blockers, ids := fillRunning(t, s)

	var queued []*blocker
	var qids []string
	for i := range 3 {
		b := newBlocker()
		id, err := s.Start(context.Background(), "test", b.run(&result{Text: fmt.Sprint("q", i)}))
		if err != nil {
			t.Fatal(err)
		}
		queued, qids = append(queued, b), append(qids, id)
	}
	for i, id := range qids {
		if snap, _ := s.Get(id); snap.State != StateQueued || snap.Position != i+1 {
			t.Fatalf("queued job %d = %+v, want position %d", i, snap, i+1)
		}
	}

	for k := range 3 {
		close(blockers[k].release)
		waitFinished(t, s, ids[k])
		if !hasStarted(queued[k], 5*time.Second) {
			t.Fatalf("queued job %d did not start after %d finished", k, k+1)
		}
		for later := k + 1; later < 3; later++ {
			if hasStarted(queued[later], 20*time.Millisecond) {
				t.Fatalf("queued job %d started before job %d", later, k)
			}
			if snap, _ := s.Get(qids[later]); snap.State != StateQueued || snap.Position != later-k {
				t.Fatalf("queued job %d = %+v, want position %d", later, snap, later-k)
			}
		}
	}
	for _, b := range append(queued, blockers[3]) {
		close(b.release)
	}
}

// TestQueueIsBounded: MaxQueued jobs wait; one more is refused with the new
// sentence and nothing of it runs.
func TestQueueIsBounded(t *testing.T) {
	s := New[*result](Options{})
	defer s.Close()
	blockers, _ := fillRunning(t, s)
	for i := range MaxQueued {
		if _, err := s.Start(context.Background(), "test", instant(&result{})); err != nil {
			t.Fatalf("queued job %d: %v", i+1, err)
		}
	}
	ran := false
	_, err := s.Start(context.Background(), "test", func(context.Context, func(string)) (*result, error) {
		ran = true
		return &result{}, nil
	})
	if !errors.Is(err, ErrTooMany) {
		t.Fatalf("Start beyond the queue error = %v, want ErrTooMany", err)
	}
	if err.Error() != "too many background jobs are running or waiting; wait for one to finish" {
		t.Errorf("message = %q", err.Error())
	}
	for _, b := range blockers {
		close(b.release)
	}
	if ran {
		t.Error("the refused job ran")
	}
}

// TestCloseFailsQueuedJobsWithoutRunningThem: Close fails every queued job
// with the shutdown sentence; none of them runs.
func TestCloseFailsQueuedJobsWithoutRunningThem(t *testing.T) {
	s := New[*result](Options{})
	blockers, _ := fillRunning(t, s)
	var ran sync.Map
	var qids []string
	for i := range 3 {
		id, err := s.Start(context.Background(), "test", func(context.Context, func(string)) (*result, error) {
			ran.Store(i, true)
			return &result{}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		qids = append(qids, id)
	}
	s.Close()
	for _, id := range qids {
		snap := waitFinished(t, s, id)
		if snap.State != StateFailed || snap.Message != ClosedSentence || snap.Position != 0 {
			t.Errorf("queued job after Close = %+v", snap)
		}
	}
	// The running jobs' contexts ended, which lets them return and frees
	// their slots; nothing may start from the queue then.
	for _, b := range blockers {
		close(b.release)
	}
	time.Sleep(50 * time.Millisecond)
	ran.Range(func(k, _ any) bool {
		t.Errorf("queued job %v ran after Close", k)
		return true
	})
}

// TestQueuedJobIsNeverEvictedOrExpired: queued jobs survive the 64-result cap
// and the result TTL; their elapsed time counts from the moment they were
// queued, and from their start it is the run time only.
func TestQueuedJobIsNeverEvictedOrExpired(t *testing.T) {
	clock := newFakeClock()
	s := New[*result](Options{Now: clock.Now})
	defer s.Close()
	blockers, ids := fillRunning(t, s)
	q := newBlocker()
	qid, err := s.Start(context.Background(), "test", q.run(&result{}))
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(TTL + time.Minute)
	if snap, err := s.Get(qid); err != nil || snap.State != StateQueued || snap.ElapsedSeconds() != int((TTL+time.Minute)/time.Second) {
		t.Fatalf("queued job after the TTL = %+v, %v", snap, err)
	}
	// Finish more jobs than the store keeps while the queued one waits.
	close(blockers[0].release)
	waitFinished(t, s, ids[0])
	if !hasStarted(q, 5*time.Second) {
		t.Fatal("the queued job did not start")
	}
	if snap, _ := s.Get(qid); snap.State != StateRunning || snap.Elapsed != 0 {
		t.Fatalf("started job = %+v, want running with 0 elapsed", snap)
	}
	clock.Advance(3 * time.Second)
	if snap, _ := s.Get(qid); snap.ElapsedSeconds() != 3 {
		t.Errorf("elapsed after start = %d s, want 3", snap.ElapsedSeconds())
	}

	close(q.release)
	for _, b := range blockers[1:] {
		close(b.release)
	}
}

// TestEvictionAndSweepSkipQueuedJobs: the 64-result cap and the TTL sweep
// look at finished jobs only; a queued job survives both.
func TestEvictionAndSweepSkipQueuedJobs(t *testing.T) {
	clock := newFakeClock()
	s := New[*result](Options{Now: clock.Now})
	defer s.Close()
	blockers, _ := fillRunning(t, s)
	qid, err := s.Start(context.Background(), "test", instant(&result{}))
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	for i := range MaxKept + 6 {
		id := fmt.Sprint("job_old", i)
		s.jobs[id] = &job[*result]{id: id, state: StateDone, started: clock.Now(), finished: clock.Now().Add(time.Duration(i) * time.Second)}
	}
	s.evictLocked()
	kept := len(s.jobs)
	s.mu.Unlock()
	if want := MaxKept + MaxRunning + 1; kept != want {
		t.Errorf("jobs after eviction = %d, want %d (the cap counts finished jobs only)", kept, want)
	}
	clock.Advance(2 * TTL)
	if snap, err := s.Get(qid); err != nil || snap.State != StateQueued {
		t.Errorf("queued job after the sweep = %+v, %v", snap, err)
	}
	for _, b := range blockers {
		close(b.release)
	}
}

func TestKeepsAtMost64ResultsAndNeverDropsARunningJob(t *testing.T) {
	clock := newFakeClock()
	s := New[*result](Options{Now: clock.Now})
	defer s.Close()

	b := newBlocker()
	runningID, err := s.Start(context.Background(), "test", b.run(&result{}))
	if err != nil {
		t.Fatal(err)
	}
	const total = MaxKept + 6
	var ids []string
	for i := range total {
		clock.Advance(time.Second)
		id, err := s.Start(context.Background(), "test", instant(&result{Text: fmt.Sprint(i)}))
		if err != nil {
			t.Fatalf("job %d: %v", i, err)
		}
		waitFinished(t, s, id)
		ids = append(ids, id)
	}

	for i, id := range ids {
		_, err := s.Get(id)
		if i < total-MaxKept {
			if !errors.Is(err, ErrUnknown) {
				t.Errorf("job %d (finished early) error = %v, want evicted", i, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("job %d (among the last %d) error = %v", i, MaxKept, err)
		}
	}
	snap, err := s.Get(runningID)
	if err != nil || snap.State != StateRunning {
		t.Fatalf("running job = %+v, %v; want still running", snap, err)
	}
	close(b.release)
	waitFinished(t, s, runningID)
}

func TestFinishedJobExpiresAfterTTL(t *testing.T) {
	clock := newFakeClock()
	s := New[*result](Options{Now: clock.Now})
	defer s.Close()
	id, err := s.Start(context.Background(), "test", instant(&result{Text: "ok"}))
	if err != nil {
		t.Fatal(err)
	}
	waitFinished(t, s, id)

	clock.Advance(TTL - time.Nanosecond)
	if _, err := s.Get(id); err != nil {
		t.Fatalf("Get just before the TTL: %v", err)
	}
	clock.Advance(time.Nanosecond)
	_, err = s.Get(id)
	if !errors.Is(err, ErrUnknown) || err.Error() != "unknown or expired job_id" {
		t.Fatalf("Get after the TTL error = %v, want %q", err, UnknownSentence)
	}
	if _, err := s.Wait(context.Background(), id, time.Second); !errors.Is(err, ErrUnknown) {
		t.Fatalf("Wait after the TTL error = %v", err)
	}
}

func TestRunningJobNeverExpires(t *testing.T) {
	clock := newFakeClock()
	s := New[*result](Options{Now: clock.Now})
	defer s.Close()
	b := newBlocker()
	id, err := s.Start(context.Background(), "test", b.run(&result{}))
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(24 * time.Hour)
	snap, err := s.Get(id)
	if err != nil {
		t.Fatalf("Get a day later: %v", err)
	}
	if snap.State != StateRunning || snap.ElapsedSeconds() != 24*3600 {
		t.Fatalf("snapshot = %+v, want running for 86400 s", snap)
	}

	// The TTL counts from the finish, not from the start.
	close(b.release)
	waitFinished(t, s, id)
	clock.Advance(TTL - time.Second)
	if _, err := s.Get(id); err != nil {
		t.Fatalf("Get within the TTL of the finish: %v", err)
	}
}

func TestUnknownID(t *testing.T) {
	s := New[*result](Options{})
	defer s.Close()
	if _, err := s.Get("job_doesnotexist"); !errors.Is(err, ErrUnknown) {
		t.Errorf("Get error = %v", err)
	}
	if _, err := s.Wait(context.Background(), "", time.Second); !errors.Is(err, ErrUnknown) {
		t.Errorf("Wait error = %v", err)
	}
}

func TestWaitTimesOutWithARunningSnapshot(t *testing.T) {
	clock := newFakeClock()
	s := New[*result](Options{Now: clock.Now})
	defer s.Close()
	release := make(chan struct{})
	staged := make(chan struct{})
	id, err := s.Start(context.Background(), "test", func(ctx context.Context, progress func(string)) (*result, error) {
		progress("fetching")
		progress("calling model")
		close(staged)
		<-release
		return &result{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-staged
	clock.Advance(7 * time.Second)

	start := time.Now()
	snap, err := s.Wait(context.Background(), id, 30*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 30*time.Millisecond {
		t.Errorf("Wait returned after %v, before its timeout", time.Since(start))
	}
	if snap.ID != id || snap.State != StateRunning || snap.Stage != "calling model" || snap.ElapsedSeconds() != 7 {
		t.Fatalf("snapshot = %+v, want running at calling model after 7 s", snap)
	}

	// A zero wait returns at once.
	snap, err = s.Wait(context.Background(), id, 0)
	if err != nil || snap.State != StateRunning {
		t.Fatalf("zero wait = %+v, %v", snap, err)
	}
	close(release)
	waitFinished(t, s, id)
}

func TestWaitReturnsWhenTheCallerContextEnds(t *testing.T) {
	s := New[*result](Options{})
	defer s.Close()
	b := newBlocker()
	id, err := s.Start(context.Background(), "test", b.run(&result{}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snap, err := s.Wait(ctx, id, time.Minute)
	if !errors.Is(err, context.Canceled) || snap.State != StateRunning {
		t.Fatalf("Wait = %+v, %v; want a running snapshot and context.Canceled", snap, err)
	}
	close(b.release)
	waitFinished(t, s, id)
}

func TestWaitReturnsDone(t *testing.T) {
	clock := newFakeClock()
	s := New[*result](Options{Now: clock.Now})
	defer s.Close()
	want := &result{Text: "the review", Structured: []byte(`{"findings":[],"summary":"ok"}`)}
	b := newBlocker()
	id, err := s.Start(context.Background(), "test", b.run(want))
	if err != nil {
		t.Fatal(err)
	}
	<-b.started
	go func() {
		clock.Advance(90 * time.Second)
		close(b.release)
	}()
	snap, err := s.Wait(context.Background(), id, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != StateDone || snap.Result != want || snap.Message != "" {
		t.Fatalf("snapshot = %+v, want done with the run's own result", snap)
	}
	if snap.Result.Text != "the review" || string(snap.Result.Structured) != `{"findings":[],"summary":"ok"}` {
		t.Errorf("result changed: %+v", snap.Result)
	}
	if snap.ElapsedSeconds() != 90 {
		t.Errorf("elapsed = %d s, want the 90 s run time", snap.ElapsedSeconds())
	}
	// A finished job answers Get and Wait the same way, again and again.
	for range 2 {
		again, err := s.Get(id)
		if err != nil || again.State != StateDone || again.Result != want {
			t.Fatalf("Get = %+v, %v", again, err)
		}
	}
}

func TestFailedJobKeepsTheClassifiedMessage(t *testing.T) {
	s := New[*result](Options{})
	defer s.Close()
	const sentence = "the LLM endpoint did not answer in time"
	id, err := s.Start(context.Background(), "test", func(context.Context, func(string)) (*result, error) {
		return &result{Text: "partial"}, errors.New(sentence)
	})
	if err != nil {
		t.Fatal(err)
	}
	snap := waitFinished(t, s, id)
	if snap.State != StateFailed || snap.Message != sentence || snap.Result != nil {
		t.Fatalf("snapshot = %+v, want failed with %q and no result", snap, sentence)
	}
	// A failed job frees its running slot.
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	if running != 0 {
		t.Errorf("running = %d after the failure", running)
	}
}

func TestWaitProgressReportsEveryStageInOrder(t *testing.T) {
	s := New[*result](Options{})
	defer s.Close()
	release := make(chan struct{})
	reached := make(chan struct{})
	id, err := s.Start(context.Background(), "pr_review", func(_ context.Context, progress func(string)) (*result, error) {
		// Two stages before anyone waits: they must not be lost.
		progress("fetching")
		progress("fetching")
		progress("preparing diff")
		close(reached)
		<-release
		progress("calling model")
		progress("rendering")
		return &result{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-reached
	go func() {
		time.Sleep(10 * time.Millisecond)
		close(release)
	}()
	var got []string
	snap, err := s.WaitProgress(context.Background(), id, 5*time.Second, func(stage string) { got = append(got, stage) })
	if err != nil || snap.State != StateDone || snap.Tag != "pr_review" {
		t.Fatalf("WaitProgress = %+v, %v", snap, err)
	}
	if strings.Join(got, "|") != "fetching|preparing diff|calling model|rendering" {
		t.Errorf("stages = %v", got)
	}

	// A later wait replays the stages; a zero wait reports them too.
	got = nil
	if _, err := s.WaitProgress(context.Background(), id, 0, func(stage string) { got = append(got, stage) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Errorf("replayed stages = %v", got)
	}
}

func TestCloseCancelsRunningJobs(t *testing.T) {
	s := New[*result](Options{})
	cancelled := make(chan error, MaxRunning)
	var ids []string
	for range MaxRunning {
		started := make(chan struct{})
		id, err := s.Start(context.Background(), "test", func(ctx context.Context, _ func(string)) (*result, error) {
			close(started)
			<-ctx.Done()
			cancelled <- ctx.Err()
			return nil, errors.New("the run was cancelled")
		})
		if err != nil {
			t.Fatal(err)
		}
		<-started
		ids = append(ids, id)
	}
	s.Close()
	for range MaxRunning {
		select {
		case err := <-cancelled:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("job context error = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Close did not cancel a running job")
		}
	}
	for _, id := range ids {
		if snap := waitFinished(t, s, id); snap.State != StateFailed {
			t.Errorf("job %s = %+v, want failed", id, snap)
		}
	}
	if _, err := s.Start(context.Background(), "test", instant(&result{})); !errors.Is(err, ErrClosed) {
		t.Errorf("Start after Close error = %v, want ErrClosed", err)
	}
	s.Close() // idempotent
}

func TestJobContextIsNotCancelledWithTheCallerContext(t *testing.T) {
	s := New[*result](Options{})
	defer s.Close()
	caller, cancelCaller := context.WithTimeout(context.Background(), time.Hour)
	release := make(chan struct{})
	id, err := s.Start(caller, "test", func(ctx context.Context, _ func(string)) (*result, error) {
		<-release
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("job context ended with the caller: %w", err)
		}
		if _, ok := ctx.Deadline(); ok {
			return nil, errors.New("job context inherited the caller's deadline")
		}
		return &result{Text: "ok"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The MCP call answers (its context ends) while the job keeps running.
	cancelCaller()
	close(release)
	snap := waitFinished(t, s, id)
	if snap.State != StateDone {
		t.Fatalf("job = %+v, want done after the caller's context ended", snap)
	}
}

// TestFinishedJobDropsItsRun checks X-10's "credentials beyond the per-call
// config": once the run returned, nothing in the store keeps the closure or
// the config it captured reachable.
func TestFinishedJobDropsItsRun(t *testing.T) {
	type callConfig struct {
		Token string
		Next  *callConfig
	}
	s := New[*result](Options{})
	defer s.Close()

	// The config and the closure exist only inside start, so the store is
	// the only thing that could keep them reachable.
	start := func() (string, weak.Pointer[callConfig]) {
		cfg := &callConfig{Token: "FAKE-token-not-a-secret"}
		id, err := s.Start(context.Background(), "test", func(context.Context, func(string)) (*result, error) {
			return &result{Text: "done with " + fmt.Sprint(len(cfg.Token))}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return id, weak.Make(cfg)
	}
	id, ref := start()
	snap := waitFinished(t, s, id)
	if snap.State != StateDone {
		t.Fatalf("job = %+v", snap)
	}
	// The run's goroutine may still be unwinding when Wait returns, and the
	// collector needs a cycle after that; under CI load this can take a
	// while, so the deadline is generous. The guarantee itself is strict.
	deadline := time.Now().Add(10 * time.Second)
	for ref.Value() != nil && time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(5 * time.Millisecond)
	}
	if ref.Value() != nil {
		t.Fatal("the per-call config is still reachable after the run finished")
	}
	if _, err := s.Get(id); err != nil {
		t.Fatalf("the finished job itself must stay: %v", err)
	}
}

// TestQueuedJobDropsItsRunWhenItEnds: a job that waited in the queue lets go
// of its closure and the config it captured once it finished, as a job that
// started at once does (X-10 unchanged).
func TestQueuedJobDropsItsRunWhenItEnds(t *testing.T) {
	type callConfig struct{ Token string }
	s := New[*result](Options{})
	defer s.Close()
	blockers, _ := fillRunning(t, s)
	queue := func() (string, weak.Pointer[callConfig]) {
		cfg := &callConfig{Token: "FAKE-token-not-a-secret"}
		id, err := s.Start(context.Background(), "test", func(context.Context, func(string)) (*result, error) {
			return &result{Text: fmt.Sprint(len(cfg.Token))}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return id, weak.Make(cfg)
	}
	id, ref := queue()
	if ref.Value() == nil {
		t.Fatal("the queued job does not hold its closure")
	}
	close(blockers[0].release)
	if snap := waitFinished(t, s, id); snap.State != StateDone {
		t.Fatalf("job = %+v", snap)
	}
	deadline := time.Now().Add(10 * time.Second)
	for ref.Value() != nil && time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(5 * time.Millisecond)
	}
	if ref.Value() != nil {
		t.Fatal("the per-call config is still reachable after the queued job finished")
	}
	for _, b := range blockers[1:] {
		close(b.release)
	}
}

// TestJobHoldsNoFunction is the deterministic half of the check above: the
// job record has no field that could keep the run closure (or the context
// tree that ends with the run) reachable.
func TestJobHoldsNoFunction(t *testing.T) {
	typ := reflect.TypeFor[job[*result]]()
	ctxType := reflect.TypeFor[context.Context]()
	for i := range typ.NumField() {
		f := typ.Field(i)
		if f.Type.Kind() == reflect.Func || f.Type.Kind() == reflect.Interface || f.Type == ctxType {
			// A queued job holds its closure and start context (X-10 as
			// amended by WP-8e); the store clears both when it starts.
			if f.Name != "run" && f.Name != "ctx" {
				t.Errorf("job.%s (%s) could keep the run or its context reachable", f.Name, f.Type)
			}
		}
	}
}

func TestConcurrentStartWaitGet(t *testing.T) {
	s := New[*result](Options{})
	defer s.Close()
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Go(func() {
			for i := range 25 {
				id, err := s.Start(context.Background(), "test", func(_ context.Context, progress func(string)) (*result, error) {
					progress("fetching")
					time.Sleep(time.Millisecond)
					progress("calling model")
					return &result{Text: fmt.Sprintf("%d/%d", g, i)}, nil
				})
				if errors.Is(err, ErrTooMany) {
					time.Sleep(time.Millisecond)
					continue
				}
				if err != nil {
					t.Error(err)
					return
				}
				if _, err := s.Get(id); err != nil && !errors.Is(err, ErrUnknown) {
					t.Error(err)
				}
				snap, err := s.WaitProgress(context.Background(), id, 5*time.Second, func(string) {})
				if errors.Is(err, ErrUnknown) {
					continue // evicted by the 64-result cap before this wait began
				}
				if err != nil {
					t.Error(err)
					return
				}
				if snap.State != StateDone || snap.Result.Text != fmt.Sprintf("%d/%d", g, i) {
					t.Errorf("job %s = %+v", id, snap)
				}
			}
		})
	}
	wg.Wait()
}
