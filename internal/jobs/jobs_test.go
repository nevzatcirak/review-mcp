package jobs

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"runtime"
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

// waitFinished waits (in real time) for a job to leave the running state.
func waitFinished(t *testing.T, s *Store[*result], id string) Snapshot[*result] {
	t.Helper()
	snap, err := s.Wait(context.Background(), id, 5*time.Second)
	if err != nil {
		t.Fatalf("Wait(%s): %v", id, err)
	}
	if snap.State == StateRunning {
		t.Fatalf("job %s still running after 5 s", id)
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

	id, err := s.Start(context.Background(), instant(&result{}))
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString(id) {
		t.Errorf("Start id = %q", id)
	}
}

func TestFifthJobRefusedUntilOneFinishes(t *testing.T) {
	s := New[*result](Options{})
	defer s.Close()
	var blockers []*blocker
	var ids []string
	for i := range MaxRunning {
		b := newBlocker()
		id, err := s.Start(context.Background(), b.run(&result{Text: fmt.Sprint(i)}))
		if err != nil {
			t.Fatalf("job %d: %v", i+1, err)
		}
		blockers = append(blockers, b)
		ids = append(ids, id)
	}

	_, err := s.Start(context.Background(), instant(&result{}))
	if !errors.Is(err, ErrTooMany) {
		t.Fatalf("5th Start error = %v, want ErrTooMany", err)
	}
	if err.Error() != "too many background jobs are running; wait for one to finish" {
		t.Errorf("5th Start message = %q", err.Error())
	}

	close(blockers[0].release)
	waitFinished(t, s, ids[0])
	if _, err := s.Start(context.Background(), instant(&result{})); err != nil {
		t.Fatalf("Start after one finished: %v", err)
	}
	for _, b := range blockers[1:] {
		close(b.release)
	}
}

func TestKeepsAtMost64ResultsAndNeverDropsARunningJob(t *testing.T) {
	clock := newFakeClock()
	s := New[*result](Options{Now: clock.Now})
	defer s.Close()

	b := newBlocker()
	runningID, err := s.Start(context.Background(), b.run(&result{}))
	if err != nil {
		t.Fatal(err)
	}
	const total = MaxKept + 6
	var ids []string
	for i := range total {
		clock.Advance(time.Second)
		id, err := s.Start(context.Background(), instant(&result{Text: fmt.Sprint(i)}))
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
	id, err := s.Start(context.Background(), instant(&result{Text: "ok"}))
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
	id, err := s.Start(context.Background(), b.run(&result{}))
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
	id, err := s.Start(context.Background(), func(ctx context.Context, progress func(string)) (*result, error) {
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
	id, err := s.Start(context.Background(), b.run(&result{}))
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
	id, err := s.Start(context.Background(), b.run(want))
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
	id, err := s.Start(context.Background(), func(context.Context, func(string)) (*result, error) {
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

func TestWaitProgressReportsEachStageOnce(t *testing.T) {
	s := New[*result](Options{})
	defer s.Close()
	next := make(chan string)
	id, err := s.Start(context.Background(), func(_ context.Context, progress func(string)) (*result, error) {
		for stage := range next {
			progress(stage)
		}
		return &result{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for _, stage := range []string{"fetching", "fetching", "preparing diff", "calling model", "rendering"} {
			next <- stage
		}
		close(next)
	}()
	var got []string
	snap, err := s.WaitProgress(context.Background(), id, 5*time.Second, func(stage string) { got = append(got, stage) })
	if err != nil || snap.State != StateDone {
		t.Fatalf("WaitProgress = %+v, %v", snap, err)
	}
	seen := map[string]bool{}
	for _, stage := range got {
		if seen[stage] {
			t.Errorf("stage %q reported twice: %v", stage, got)
		}
		seen[stage] = true
	}
	if len(got) == 0 || got[len(got)-1] != "rendering" {
		t.Errorf("stages = %v, want them to end with rendering", got)
	}
}

func TestCloseCancelsRunningJobs(t *testing.T) {
	s := New[*result](Options{})
	cancelled := make(chan error, MaxRunning)
	var ids []string
	for range MaxRunning {
		started := make(chan struct{})
		id, err := s.Start(context.Background(), func(ctx context.Context, _ func(string)) (*result, error) {
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
	if _, err := s.Start(context.Background(), instant(&result{})); !errors.Is(err, ErrClosed) {
		t.Errorf("Start after Close error = %v, want ErrClosed", err)
	}
	s.Close() // idempotent
}

func TestJobContextIsNotCancelledWithTheCallerContext(t *testing.T) {
	s := New[*result](Options{})
	defer s.Close()
	caller, cancelCaller := context.WithTimeout(context.Background(), time.Hour)
	release := make(chan struct{})
	id, err := s.Start(caller, func(ctx context.Context, _ func(string)) (*result, error) {
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
		id, err := s.Start(context.Background(), func(context.Context, func(string)) (*result, error) {
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
	for range 10 {
		runtime.GC()
		if ref.Value() == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ref.Value() != nil {
		t.Fatal("the per-call config is still reachable after the run finished")
	}
	if _, err := s.Get(id); err != nil {
		t.Fatalf("the finished job itself must stay: %v", err)
	}
}

func TestConcurrentStartWaitGet(t *testing.T) {
	s := New[*result](Options{})
	defer s.Close()
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Go(func() {
			for i := range 25 {
				id, err := s.Start(context.Background(), func(_ context.Context, progress func(string)) (*result, error) {
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
