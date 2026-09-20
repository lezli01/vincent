package agent_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
)

// The skill cache's tests (§9.6, task 124, 124.9, #505). They live in
// agent_test because agenttest.StubSkills is what counts the probes, and
// agenttest imports agent. The clock is injected through export_test.go, the
// way catalog_test.go injects CatalogCache's.

// testClock is a clock a test moves by hand. It is guarded because a cache
// under concurrent Lookups reads it from every goroutine.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newSkillCache() (*agent.SkillCache, *testClock) {
	c := agent.NewSkillCache()
	clk := &testClock{t: time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)}
	agent.SetSkillCacheClock(c, clk.now)
	return c, clk
}

// listed is a scripted listing of skills with these names, in this order.
func listed(names ...string) agent.SkillList {
	var l agent.SkillList
	for _, n := range names {
		l.Skills = append(l.Skills, agent.Skill{Name: n, Description: n + " (project)"})
	}
	return l
}

// gatedSkills is a StubSkills whose probes can be held open. ListSkills asks
// the stub first, so the answer is fixed when the probe starts, then reports
// the start on started and waits for release. Path reports each Lookup's
// arrival on arrived: the cache resolves the binary identity once per Lookup,
// after it has marked the call's arrival. A nil channel is skipped.
type gatedSkills struct {
	*agenttest.StubSkills
	arrived chan struct{}
	started chan struct{}
	release chan struct{}
}

func (g *gatedSkills) Path() (string, error) {
	if g.arrived != nil {
		g.arrived <- struct{}{}
	}
	return g.StubSkills.Path()
}

func (g *gatedSkills) ListSkills(ctx context.Context, q agent.SkillQuery) (agent.SkillList, error) {
	l, err := g.StubSkills.ListSkills(ctx, q)
	if g.started != nil {
		g.started <- struct{}{}
	}
	if g.release != nil {
		<-g.release
	}
	return l, err
}

// fileSkills is a StubSkills whose binary is a real file, so a test can move
// its mtime and with it the binary identity.
type fileSkills struct {
	*agenttest.StubSkills
	path string
}

func (f *fileSkills) Path() (string, error) { return f.path, nil }

// namedSkills is a StubSkills under another adapter name.
type namedSkills struct {
	*agenttest.StubSkills
	name string
}

func (n *namedSkills) Name() string { return n.name }

// receive waits for one signal on ch, failing the test rather than hanging it.
func receive(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func wantCalls(t *testing.T, s *agenttest.StubSkills, want int) {
	t.Helper()
	if got := s.Calls(); got != want {
		t.Fatalf("ListSkills called %d times, want %d", got, want)
	}
}

// TestSkillCacheTrustsACleanListForItsTTL: a second Lookup within skillTTL
// spawns nothing, the first one after it probes again, and refresh bypasses
// the TTL outright.
func TestSkillCacheTrustsACleanListForItsTTL(t *testing.T) {
	c, clk := newSkillCache()
	stub := &agenttest.StubSkills{}
	stub.Script(listed("review"), nil)
	dir := t.TempDir()
	t0 := clk.now()

	first := c.Lookup(t.Context(), stub, dir, false)
	wantCalls(t, stub, 1)
	if first.Verdict != agent.InputSupported || !first.ProbedAt.Equal(t0) || first.ProbeError != "" || first.Reason != "" {
		t.Fatalf("first answer = %+v, want a clean supported list probed at %v", first, t0)
	}

	clk.advance(agent.SkillTTL - time.Nanosecond)
	if got := c.Lookup(t.Context(), stub, dir, false); !reflect.DeepEqual(got, first) {
		t.Fatalf("answer within the TTL = %+v, want the cached %+v", got, first)
	}
	wantCalls(t, stub, 1)

	clk.advance(time.Nanosecond)
	if got := c.Lookup(t.Context(), stub, dir, false); !got.ProbedAt.Equal(clk.now()) {
		t.Fatalf("answer at the TTL probed at %v, want a new probe at %v", got.ProbedAt, clk.now())
	}
	wantCalls(t, stub, 2)

	c.Lookup(t.Context(), stub, dir, true)
	wantCalls(t, stub, 3)
}

// TestSkillCacheRefreshIsSingleFlight: N concurrent refreshes of one key cost
// one ListSkills, and every caller is served its answer. Each caller reports
// its arrival through Path before the one probe is let finish, so every one
// of them arrived before it did and none may probe again.
func TestSkillCacheRefreshIsSingleFlight(t *testing.T) {
	const n = 16
	c, _ := newSkillCache()
	stub := &agenttest.StubSkills{}
	stub.Script(listed("review", "deploy"), nil)
	g := &gatedSkills{StubSkills: stub, arrived: make(chan struct{}, n), release: make(chan struct{})}
	dir := t.TempDir()

	answers := make([]agent.SkillAnswer, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { answers[i] = c.Lookup(t.Context(), g, dir, true) })
	}
	for range n {
		receive(t, g.arrived, "every Lookup to arrive")
	}
	close(g.release)
	wg.Wait()

	wantCalls(t, stub, 1)
	for i, a := range answers {
		if a.Verdict != agent.InputSupported || !reflect.DeepEqual(a.Skills, listed("review", "deploy").Skills) {
			t.Errorf("caller %d answered %+v, want the one probe's list", i, a)
		}
	}
}

// TestSkillCacheReaderNeverWaitsBehindAProbe: a non-refresh Lookup that finds
// a fresh answer is served while a refresh of the same key is still probing.
func TestSkillCacheReaderNeverWaitsBehindAProbe(t *testing.T) {
	c, _ := newSkillCache()
	stub := &agenttest.StubSkills{}
	stub.Script(listed("review"), nil)
	dir := t.TempDir()
	primed := c.Lookup(t.Context(), stub, dir, false)

	// gatedSkills shares the stub's name and path, so it is the same key.
	g := &gatedSkills{StubSkills: stub, started: make(chan struct{}, 1), release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Lookup(t.Context(), g, dir, true)
	}()
	receive(t, g.started, "the refresh to start probing")

	served := make(chan agent.SkillAnswer, 1)
	go func() { served <- c.Lookup(t.Context(), stub, dir, false) }()
	select {
	case got := <-served:
		if !reflect.DeepEqual(got, primed) {
			t.Errorf("reader served %+v, want the fresh %+v", got, primed)
		}
	case <-time.After(10 * time.Second):
		t.Error("a reader of a fresh answer waited behind a running probe")
	}
	close(g.release)
	receive(t, done, "the refresh to finish")
	wantCalls(t, stub, 2)
}

// TestSkillCacheFailureWithNoListIsUnknown: a failed probe with nothing
// earlier to keep answers unknown with the error, is trusted for
// skillFailureTTL and no longer.
func TestSkillCacheFailureWithNoListIsUnknown(t *testing.T) {
	c, clk := newSkillCache()
	stub := &agenttest.StubSkills{}
	stub.Script(agent.SkillList{}, errors.New("claude initialize: timed out after 10s"))
	dir := t.TempDir()

	want := agent.SkillAnswer{Verdict: agent.InputUnknown, ProbeError: "claude initialize: timed out after 10s"}
	if got := c.Lookup(t.Context(), stub, dir, false); !reflect.DeepEqual(got, want) {
		t.Fatalf("failed first probe answered %+v, want %+v", got, want)
	}
	wantCalls(t, stub, 1)

	clk.advance(agent.SkillFailureTTL - time.Nanosecond)
	if got := c.Lookup(t.Context(), stub, dir, false); !reflect.DeepEqual(got, want) {
		t.Fatalf("answer within the failure TTL = %+v, want %+v", got, want)
	}
	wantCalls(t, stub, 1)

	clk.advance(time.Nanosecond)
	stub.Script(listed("review"), nil)
	if got := c.Lookup(t.Context(), stub, dir, false); got.Verdict != agent.InputSupported || got.ProbeError != "" {
		t.Fatalf("answer after the failure TTL = %+v, want a clean list", got)
	}
	wantCalls(t, stub, 2)
}

// TestSkillCacheFailureKeepsThePreviousList holds T4.22 for listings: a
// failed probe after a clean one keeps its verdict, list and ProbedAt, sets
// ProbeError, and is retried after skillFailureTTL rather than skillTTL.
func TestSkillCacheFailureKeepsThePreviousList(t *testing.T) {
	c, clk := newSkillCache()
	stub := &agenttest.StubSkills{}
	list := listed("review", "deploy")
	list.Problems = []agent.SkillProblem{{Path: "/x/SKILL.md", Message: "missing name"}}
	stub.Script(list, nil)
	dir := t.TempDir()
	clean := c.Lookup(t.Context(), stub, dir, false)

	clk.advance(agent.SkillTTL)
	stub.Script(agent.SkillList{}, errors.New("exit status 1"))
	want := clean
	want.ProbeError = "exit status 1"
	if got := c.Lookup(t.Context(), stub, dir, false); !reflect.DeepEqual(got, want) {
		t.Fatalf("failed re-probe answered %+v, want the kept %+v", got, want)
	}
	wantCalls(t, stub, 2)

	clk.advance(agent.SkillFailureTTL - time.Nanosecond)
	if got := c.Lookup(t.Context(), stub, dir, false); !reflect.DeepEqual(got, want) {
		t.Fatalf("answer within the failure TTL = %+v, want %+v", got, want)
	}
	wantCalls(t, stub, 2)

	clk.advance(time.Nanosecond)
	c.Lookup(t.Context(), stub, dir, false)
	wantCalls(t, stub, 3)
}

// TestSkillCacheUnsupportedIsACleanNo: ErrSkillsUnsupported, wrapped,
// answers unsupported with its wrapped text as the reason and is trusted for
// the whole clean TTL. No ordinary error ever answers unsupported.
func TestSkillCacheUnsupportedIsACleanNo(t *testing.T) {
	c, clk := newSkillCache()
	stub := &agenttest.StubSkills{}
	err := fmt.Errorf("claude 2.0.1 is older than 2.1.277: %w", agent.ErrSkillsUnsupported)
	stub.Script(agent.SkillList{}, err)
	dir := t.TempDir()

	want := agent.SkillAnswer{Verdict: agent.InputUnsupported, Reason: err.Error(), ProbedAt: clk.now()}
	if got := c.Lookup(t.Context(), stub, dir, false); !reflect.DeepEqual(got, want) {
		t.Fatalf("unsupported build answered %+v, want %+v", got, want)
	}
	clk.advance(agent.SkillTTL - time.Nanosecond)
	if got := c.Lookup(t.Context(), stub, dir, false); !reflect.DeepEqual(got, want) {
		t.Fatalf("answer within the clean TTL = %+v, want %+v", got, want)
	}
	wantCalls(t, stub, 1)

	for _, ordinary := range []error{
		errors.New("this agent CLI cannot list the skills it would load"),
		fmt.Errorf("claude initialize: %w", context.DeadlineExceeded),
		errors.New("unparseable initialize response"),
	} {
		stub.Script(agent.SkillList{}, ordinary)
		got := c.Lookup(t.Context(), stub, t.TempDir(), false)
		if got.Verdict != agent.InputUnknown || got.Reason != "" || got.ProbeError != ordinary.Error() {
			t.Errorf("%q answered %+v, want unknown with the error as probe_error", ordinary, got)
		}
	}
}

// TestSkillCacheCallerCancellationIsNotStored: a probe that failed because
// its own caller hung up answers that caller, and stores nothing — with or
// without an earlier list, the next Lookup asks again.
func TestSkillCacheCallerCancellationIsNotStored(t *testing.T) {
	c, clk := newSkillCache()
	stub := &agenttest.StubSkills{}
	dir := t.TempDir()
	gone, cancel := context.WithCancel(t.Context())
	cancel()

	stub.Script(agent.SkillList{}, context.Canceled)
	if got := c.Lookup(gone, stub, dir, false); got.Verdict != agent.InputUnknown || got.ProbeError == "" {
		t.Fatalf("hung-up caller answered %+v, want unknown with a probe error", got)
	}
	stub.Script(listed("review"), nil)
	clean := c.Lookup(t.Context(), stub, dir, false)
	wantCalls(t, stub, 2)

	clk.advance(agent.SkillTTL)
	stub.Script(agent.SkillList{}, context.Canceled)
	if got := c.Lookup(gone, stub, dir, true); got.Verdict != agent.InputSupported || got.ProbeError == "" {
		t.Fatalf("hung-up refresh answered %+v, want the kept list with a probe error", got)
	}
	stub.Script(listed("review", "deploy"), nil)
	if got := c.Lookup(t.Context(), stub, dir, false); reflect.DeepEqual(got, clean) || got.ProbeError != "" {
		t.Fatalf("next Lookup answered %+v, want a new clean probe", got)
	}
	wantCalls(t, stub, 4)
}

// TestSkillCacheEvictsTheLeastRecentlyUsed: the 65th distinct key evicts the
// least recently used one, and a hit counts as a use.
func TestSkillCacheEvictsTheLeastRecentlyUsed(t *testing.T) {
	c, _ := newSkillCache()
	stub := &agenttest.StubSkills{}
	stub.Script(listed("review"), nil)
	base := t.TempDir()
	dir := func(i int) string { return filepath.Join(base, strconv.Itoa(i)) }

	for i := range agent.SkillCacheMax {
		c.Lookup(t.Context(), stub, dir(i), false)
	}
	wantCalls(t, stub, agent.SkillCacheMax)

	// A hit on the oldest makes dir(1) the least recently used.
	c.Lookup(t.Context(), stub, dir(0), false)
	wantCalls(t, stub, agent.SkillCacheMax)
	c.Lookup(t.Context(), stub, dir(agent.SkillCacheMax), false)
	wantCalls(t, stub, agent.SkillCacheMax+1)

	c.Lookup(t.Context(), stub, dir(0), false)
	c.Lookup(t.Context(), stub, dir(agent.SkillCacheMax), false)
	wantCalls(t, stub, agent.SkillCacheMax+1)
	c.Lookup(t.Context(), stub, dir(1), false)
	wantCalls(t, stub, agent.SkillCacheMax+2)
}

// TestSkillCacheInvalidateDropsOneDirectory: Invalidate drops the directory
// for every adapter, cleaned as Lookup cleans it, and leaves every other
// directory alone. A nil cache's Invalidate is a no-op.
func TestSkillCacheInvalidateDropsOneDirectory(t *testing.T) {
	c, _ := newSkillCache()
	stub := &agenttest.StubSkills{}
	stub.Script(listed("review"), nil)
	other := &namedSkills{StubSkills: &agenttest.StubSkills{}, name: "otherskills"}
	other.Script(listed("deploy"), nil)
	a, b := t.TempDir(), t.TempDir()

	c.Lookup(t.Context(), stub, a, false)
	c.Lookup(t.Context(), other, a, false)
	c.Lookup(t.Context(), stub, b, false)
	wantCalls(t, stub, 2)
	wantCalls(t, other.StubSkills, 1)

	c.Invalidate(a + string(filepath.Separator) + ".")
	c.Lookup(t.Context(), stub, b, false)
	wantCalls(t, stub, 2)
	c.Lookup(t.Context(), stub, a, false)
	c.Lookup(t.Context(), other, a, false)
	wantCalls(t, stub, 3)
	wantCalls(t, other.StubSkills, 2)

	var none *agent.SkillCache
	none.Invalidate(a)
}

// TestSkillCacheInvalidateOutlivesAProbeInFlight: a probe that started before
// an Invalidate answers its caller, but does not repopulate the directory
// with the list it started on.
func TestSkillCacheInvalidateOutlivesAProbeInFlight(t *testing.T) {
	c, _ := newSkillCache()
	stub := &agenttest.StubSkills{}
	stub.Script(listed("stale"), nil)
	g := &gatedSkills{StubSkills: stub, started: make(chan struct{}, 1), release: make(chan struct{})}
	dir := t.TempDir()

	inFlight := make(chan agent.SkillAnswer, 1)
	go func() { inFlight <- c.Lookup(t.Context(), g, dir, false) }()
	receive(t, g.started, "the probe to start")
	c.Invalidate(dir)
	stub.Script(listed("fresh"), nil)
	close(g.release)

	select {
	case got := <-inFlight:
		if !reflect.DeepEqual(got.Skills, listed("stale").Skills) {
			t.Fatalf("in-flight caller answered %+v, want its own probe's list", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the in-flight Lookup never answered")
	}
	got := c.Lookup(t.Context(), stub, dir, false)
	wantCalls(t, stub, 2)
	if !reflect.DeepEqual(got.Skills, listed("fresh").Skills) {
		t.Fatalf("Lookup after the invalidation answered %+v, want a new probe's list", got)
	}
}

// TestSkillCacheNewBinaryIsAMiss: moving the binary's mtime is a new binary
// identity, so an upgraded CLI is asked at once rather than after the TTL.
func TestSkillCacheNewBinaryIsAMiss(t *testing.T) {
	c, _ := newSkillCache()
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fileSkills{StubSkills: &agenttest.StubSkills{}, path: bin}
	f.Script(listed("review"), nil)
	dir := t.TempDir()

	c.Lookup(t.Context(), f, dir, false)
	c.Lookup(t.Context(), f, dir, false)
	wantCalls(t, f.StubSkills, 1)

	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(bin, later, later); err != nil {
		t.Fatal(err)
	}
	c.Lookup(t.Context(), f, dir, false)
	wantCalls(t, f.StubSkills, 2)
}

// TestSkillCacheServesTheCLIsOwnWords: the order and the duplicate names come
// back exactly as the CLI reported them, the probe is asked about the cleaned
// directory on the host with the daemon's environment, an unclean spelling of
// the same directory is the same key, and an empty list is none.
func TestSkillCacheServesTheCLIsOwnWords(t *testing.T) {
	c, _ := newSkillCache()
	stub := &agenttest.StubSkills{}
	list := agent.SkillList{
		Skills: []agent.Skill{
			{Name: "review", Description: "Review the diff (project)", ArgumentHint: "[base]"},
			{Name: "deploy", Scope: "repo", Path: "/r/.agents/skills/deploy/SKILL.md"},
			{Name: "review", Description: "Review the diff (user)", Aliases: []string{"rv"}},
		},
		Problems: []agent.SkillProblem{{Path: "/r/.agents/skills/bad/SKILL.md", Message: "missing name"}},
	}
	stub.Script(list, nil)
	dir := t.TempDir()

	got := c.Lookup(t.Context(), stub, dir, false)
	if !reflect.DeepEqual(got.Skills, list.Skills) || !reflect.DeepEqual(got.Problems, list.Problems) {
		t.Fatalf("answered %+v / %+v, want exactly %+v / %+v", got.Skills, got.Problems, list.Skills, list.Problems)
	}
	c.Lookup(t.Context(), stub, dir+string(filepath.Separator)+".", false)
	wantCalls(t, stub, 1)
	if q := stub.Queries(); !reflect.DeepEqual(q, []agent.SkillQuery{{WorkDir: dir}}) {
		t.Fatalf("queries = %+v, want one for %s on the host with the daemon's env", q, dir)
	}

	stub.Script(agent.SkillList{Skills: []agent.Skill{}, Problems: []agent.SkillProblem{}}, nil)
	empty := c.Lookup(t.Context(), stub, dir, true)
	if empty.Verdict != agent.InputSupported || empty.Skills != nil || empty.Problems != nil {
		t.Fatalf("empty list answered %+v, want supported with nil skills and problems", empty)
	}
}

// TestSkillCacheNonListerIsUnsupported: an adapter without SkillLister
// answers unsupported with no reason of the cache's own, and is never asked.
// It is proven against agenttest.StubNoSkills, not a shipped adapter (task 124
// decision 15).
func TestSkillCacheNonListerIsUnsupported(t *testing.T) {
	c, _ := newSkillCache()
	for _, refresh := range []bool{false, true} {
		got := c.Lookup(t.Context(), agenttest.StubNoSkills{}, t.TempDir(), refresh)
		if !reflect.DeepEqual(got, agent.SkillAnswer{Verdict: agent.InputUnsupported}) {
			t.Errorf("refresh=%v: StubNoSkills answered %+v, want a bare unsupported", refresh, got)
		}
	}
}
