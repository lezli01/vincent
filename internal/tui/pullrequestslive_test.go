package tui

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// pullListings is every recorded GET of a project's pull-request listing.
func pullListings(rec *requestRecorder) []string {
	var out []string
	for _, r := range rec.matching("GET /v1/projects/") {
		if strings.Contains(r, "/github/pulls") {
			out = append(out, r)
		}
	}
	return out
}

// exactly is the recorded requests equal to line.
func exactly(rec *requestRecorder, line string) []string {
	var out []string
	for _, r := range rec.matching(line) {
		if r == line {
			out = append(out, r)
		}
	}
	return out
}

// pullsView is the takeover as the root holds it.
func pullsView(t *testing.T, h *newTaskLiveHarness) *pullRequestsView {
	t.Helper()
	v, ok := h.m.views[viewPullRequests].(*pullRequestsView)
	if !ok {
		t.Fatalf("view %d is %T, want *pullRequestsView", viewPullRequests, h.m.views[viewPullRequests])
	}
	return v
}

// A project with a github.com origin and a working credential puts the nav
// row on the palette and fills the view; one without does neither. The view
// lists the selected project alone, with one listing and one task read per
// load (task 132.11), and a switch to a project without GitHub keeps the
// view up with that project's reason and lists nothing for it.
func TestPullRequestsListsOnlyTheSelectedProject(t *testing.T) {
	pullRequestsListOnlyTheSelectedProject(t, nil)
}

// The same, with an event-driven refresh in flight when the requests are
// counted (issue #734). The takeover refreshes on any project.* event, and
// the second project's project.created starts one that a loaded runner can
// still be sending after the probe wait: its stamp is then counted as before
// `R` and its listing and task read as after, and the per-load tally never
// balances (macOS CI timed out on it). Holding the listing of a refresh
// started here until `R`'s own listing arrives makes that happen on every run
// instead of on a slow one. The hold also lifts on its own after a moment, so
// a count that waits for the refresh to land first is not held up by it.
func TestPullRequestsListsOnlyTheSelectedProjectWithARefreshInFlight(t *testing.T) {
	pullRequestsListOnlyTheSelectedProject(t, &requestGate{match: func(line string) bool {
		return strings.HasPrefix(line, "GET /v1/projects/") && strings.Contains(line, "/github/pulls")
	}})
}

// requestGate holds, once armed, the first request match accepts until the
// next one arrives or release is called — ahead of any recorder it wraps, so
// a held request is not yet counted.
type requestGate struct {
	match  func(line string) bool
	mu     sync.Mutex
	armed  bool
	held   chan struct{}
	opened chan struct{}
	once   sync.Once
}

func (g *requestGate) arm() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.armed, g.held, g.opened = true, make(chan struct{}), make(chan struct{})
}

func (g *requestGate) release() { g.once.Do(func() { close(g.opened) }) }

func (g *requestGate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		g.mu.Lock()
		var opened chan struct{}
		if g.held != nil && g.match(req.Method+" "+req.URL.RequestURI()) {
			if g.armed {
				g.armed = false
				opened = g.opened
				close(g.held)
			} else {
				g.release()
			}
		}
		g.mu.Unlock()
		if opened != nil {
			<-opened
		}
		next.ServeHTTP(w, req)
	})
}

// pullRequestsListOnlyTheSelectedProject is the test above, with gate, when
// set, holding the listing of a refresh started just before the count.
func pullRequestsListOnlyTheSelectedProject(t *testing.T, gate *requestGate) {
	rec := &requestRecorder{}
	wrap := rec.wrap
	if gate != nil {
		wrap = func(next http.Handler) http.Handler { return gate.wrap(rec.wrap(next)) }
	}
	h, _ := newGitHubLiveHarness(t, liveOptions{remote: ghLiveOrigin, wrap: wrap})
	h.p.until(10*time.Second, "the GitHub probes to answer", func() bool {
		return h.m.githubAvailable()
	})
	v := pullsView(t, h)
	h.send(selectViewMsg{id: viewPullRequests})
	h.p.until(10*time.Second, "the pull-request listing", func() bool {
		return v.loaded && !v.loading && len(v.rows()) > 0
	})

	// A second project, with no origin at all: registered mid-session, so
	// the root re-probes on its project.created.
	other := &store.Project{Name: "zz-plain", Path: testrepo.Init(t, "main"), DefaultBranch: "main"}
	if err := h.st.CreateProject(context.Background(), other); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	h.p.until(10*time.Second, "the second project's probe", func() bool {
		_, ok := githubStatusFor(h.m.github, other.ID)
		return ok
	})
	if gate != nil {
		gate.arm()
		p, err := h.st.GetProject(context.Background(), h.projectID)
		if err != nil {
			t.Fatalf("GetProject: %v", err)
		}
		if err := h.st.UpdateProject(context.Background(), p); err != nil {
			t.Fatalf("UpdateProject: %v", err)
		}
		h.p.until(10*time.Second, "a refresh's listing to be held", func() bool {
			select {
			case <-gate.held:
				return true
			default:
				return false
			}
		})
		time.AfterFunc(500*time.Millisecond, gate.release)
	}

	// Per load: exactly one listing, for the selected project, and one task
	// read scoped to it. The board's own listing is unscoped and the
	// archived board's carries archived=, so the exact query is this view's.
	// An event-driven refresh may overlap `R`, so requests are counted
	// against the loads the view issued rather than against one.
	scopedTasks := "GET /v1/tasks?project_id=" + strconv.FormatInt(h.projectID, 10)
	beforeLoads := v.stamps.issued
	beforePulls, beforeTasks := len(pullListings(rec)), len(exactly(rec, scopedTasks))
	h.sendKey(keyPress("R"))
	h.p.until(10*time.Second, "one listing and one task read per load", func() bool {
		loads := int(v.stamps.issued - beforeLoads)
		return loads > 0 && !v.loading &&
			len(pullListings(rec))-beforePulls == loads &&
			len(exactly(rec, scopedTasks))-beforeTasks == loads
	})
	for _, p := range pullListings(rec) {
		if !strings.Contains(p, "/v1/projects/"+strconv.FormatInt(h.projectID, 10)+"/") {
			t.Errorf("a listing was made for another project: %s", p)
		}
	}

	// A switch to the project without GitHub keeps the view, shows its
	// reason, and lists nothing for it.
	h.p.push(h.m.selectProject(apiclient.Project{ID: other.ID, Name: other.Name}, "test"))
	h.p.until(10*time.Second, "the reason in place of the rows", func() bool {
		return strings.Contains(v.render(160, 30), "This project has no usable GitHub integration")
	})
	if h.m.active != viewPullRequests {
		t.Fatalf("a switch left the pull-requests view for view %d", h.m.active)
	}
	if h.m.githubAvailable() {
		t.Error("the gate still says available on a project whose probe said no")
	}
	for _, p := range pullListings(rec) {
		if strings.Contains(p, "/v1/projects/"+strconv.FormatInt(other.ID, 10)+"/") {
			t.Fatalf("a listing was made for the project without GitHub: %s", p)
		}
	}

	// Switching back reloads.
	h.p.push(h.m.selectProject(apiclient.Project{ID: h.projectID, Name: "live"}, "test"))
	h.p.until(10*time.Second, "the GitHub project's rows again", func() bool {
		return v.loaded && len(v.rows()) > 0
	})

	out := v.render(160, 30)
	// Open only: the merged row is in fakegh's corpus and must not be here,
	// which is the listing's own contract.
	if !strings.Contains(out, "Add a thing") {
		t.Errorf("the open pull request is not listed:\n%s", out)
	}
	if strings.Contains(out, "already merged") {
		t.Errorf("the merged pull request leaked into an open-only listing:\n%s", out)
	}
	if !strings.Contains(out, "draft") {
		t.Errorf("the draft's folded status word is missing:\n%s", out)
	}
}

// A reconciler tick that links a pull request re-renders an open view with
// no keypress: the root broadcasts to every view, active or not.
func TestPullRequestsRefreshesOnAReconcilerTick(t *testing.T) {
	h, _ := newGitHubLiveHarness(t, liveOptions{remote: ghLiveOrigin})
	h.p.until(10*time.Second, "the GitHub probes to answer", func() bool {
		return h.m.githubAvailable()
	})
	v := pullsView(t, h)
	h.p.until(10*time.Second, "the pull-request listing", func() bool {
		return v.loaded && len(v.rows()) > 0
	})
	if strings.Contains(v.render(160, 30), "task #") {
		t.Fatal("a row is claimed before anything linked one")
	}

	task := &store.Task{
		ProjectID: h.projectID, Title: "Add a thing", WorkflowName: "implement",
		BaseBranch: "main", BranchName: "vincent/1-add-a-thing",
		State: store.TaskQueued,
	}
	if err := h.st.CreateTask(context.Background(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	// What the daemon's reconciler writes on a head-branch match. The store
	// publishes task.github_pull_changed, which is the note the view acts on.
	if _, err := h.st.SetTaskGitHubPull(context.Background(), task.ID, &github.PullLink{
		Repo: "octo/repo", Number: 412, Source: "auto", LinkedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SetTaskGitHubPull: %v", err)
	}

	h.p.until(10*time.Second, "the linked row to appear with no keypress", func() bool {
		return strings.Contains(v.render(160, 30), "(auto)")
	})
}

// `P` on the takeover: the picker of tasks that could have a pull request and
// do not, against the real handlers (task 069).
//
// This screen has no task rows and is not given any — its question is "what is
// open in this project", and a task with no pull request is not an
// open pull request. So the offer is a picker, and choosing a task navigates
// to that task's workspace with the form up: the offer to create still lives
// in the workspace (052 decision 6), widened rather than reversed.
func TestPullRequestsCreatePickerOffersOnlyEligibleTasks(t *testing.T) {
	h, _ := newGitHubLiveHarness(t, liveOptions{remote: ghLiveOrigin})
	h.p.until(10*time.Second, "the GitHub probes to answer", func() bool {
		return h.m.githubAvailable()
	})
	v := pullsView(t, h)
	h.p.until(10*time.Second, "the pull-request listing", func() bool {
		return v.loaded && len(v.rows()) > 0
	})

	// Eligible: a branch, and no pull request claiming it.
	eligible := &store.Task{
		ProjectID: h.projectID, Title: "Not yet on GitHub", WorkflowName: "implement",
		BaseBranch: "main", BranchName: "vincent/7-not-yet", State: store.TaskQueued,
	}
	if err := h.st.CreateTask(context.Background(), eligible, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	// Not eligible: fakegh's open pull request #412 has this head branch, so
	// the listing claims it.
	claimed := &store.Task{
		ProjectID: h.projectID, Title: "Already has one", WorkflowName: "implement",
		BaseBranch: "main", BranchName: "vincent/1-add-a-thing", State: store.TaskQueued,
	}
	if err := h.st.CreateTask(context.Background(), claimed, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := h.st.SetTaskGitHubPull(context.Background(), claimed.ID, &github.PullLink{
		Repo: "octo/repo", Number: 412, Source: "auto", LinkedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SetTaskGitHubPull: %v", err)
	}
	h.p.until(10*time.Second, "the claim to reach the view", func() bool {
		return strings.Contains(v.render(160, 30), "(auto)")
	})

	v.updateKey(keyPress("P"))
	if v.picker == nil {
		t.Fatal("P did not open a picker")
	}
	out := strings.Join(v.picker.renderBody(), "\n")
	if !strings.Contains(out, "Not yet on GitHub") {
		t.Errorf("the eligible task is missing from the picker:\n%s", out)
	}
	if strings.Contains(out, "Already has one") {
		t.Errorf("a task that already has a pull request is offered:\n%s", out)
	}
	// Choosing it hands the intent to the workspace rather than opening a
	// second copy of the form here.
	sel, ok := drain(v.openTaskWithPRForm(eligible.ID)).(selectTaskMsg)
	if !ok || sel.id != eligible.ID || !sel.openPR {
		t.Fatalf("choosing a task produced %#v", sel)
	}
}
