package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Task 132.5: the {project, seq} load stamp and the client-side event filter.

func TestLoadStampsNextIncreases(t *testing.T) {
	var s loadStamps
	a, b := s.next(1), s.next(1)
	if b.seq <= a.seq {
		t.Fatalf("next went %d then %d; want an increasing seq", a.seq, b.seq)
	}
	if c := s.next(2); c.seq <= b.seq || c.project != 2 {
		t.Fatalf("next(2) = %+v after %+v", c, b)
	}
}

func TestLoadStampsAccepts(t *testing.T) {
	var s loadStamps
	old := s.next(1)
	cur := s.next(1)
	s.apply(cur)
	newer := s.next(1)
	if s.accepts(old) {
		t.Error("an older seq was accepted")
	}
	if s.accepts(cur) {
		t.Error("the seq already applied was accepted again")
	}
	if !s.accepts(newer) {
		t.Error("a newer seq for the current project was rejected")
	}
	if s.accepts(loadStamp{project: 2, seq: newer.seq + 1}) {
		t.Error("a stamp for a foreign project was accepted")
	}
	if !s.accepts(loadStamp{}) {
		t.Error("the zero stamp is untracked and must be accepted")
	}
}

func pid(id int64) *int64 { return &id }

func TestForProject(t *testing.T) {
	cases := []struct {
		name    string
		typ     string
		project *int64
		sel     int64
		want    bool
	}{
		{"nil project passes", "issue.created", nil, 2, true},
		{"matching project passes", "issue.created", pid(2), 2, true},
		{"foreign project is rejected", "issue.created", pid(1), 2, false},
		{"nothing selected passes everything", "issue.created", pid(1), 0, true},
		{"foreign project.deleted passes", "project.deleted", pid(1), 2, true},
		{"foreign project.updated passes", "project.updated", pid(1), 2, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := apiclient.Event{Type: c.typ, ProjectID: c.project}
			if got := forProject(ev, c.sel); got != c.want {
				t.Errorf("forProject = %v, want %v", got, c.want)
			}
		})
	}
}

// deadClient is a client whose commands are built but never meant to reach
// anything: a test here asserts that a load was issued, not what it read.
func deadClient() *apiclient.Client { return apiclient.New("http://127.0.0.1:1", "t") }

// stampedView is one stamped list view under the generic tests below: how
// to point it at a project, how to deliver a load stamped for one, and which
// marker the rows on screen carry.
type stampedView struct {
	name    string
	new     func() (setProject func(projectSel) tea.Cmd, load func() tea.Cmd, deliver func(loadStamp, string), shown func() string)
	skipSel bool
}

func stampedViews() []stampedView {
	return []stampedView{
		{name: "board", new: func() (func(projectSel) tea.Cmd, func() tea.Cmd, func(loadStamp, string), func() string) {
			b := newBoard()
			b.client = deadClient()
			return b.setProject, b.loadCmd,
				func(st loadStamp, mark string) {
					b.update(boardLoadedMsg{stamp: st, tasks: []apiclient.Task{{ID: 1, Title: mark}}})
				},
				func() string { return firstTitle(b.tasks) }
		}},
		{name: "archived board", new: func() (func(projectSel) tea.Cmd, func() tea.Cmd, func(loadStamp, string), func() string) {
			b := newArchivedBoard()
			b.client = deadClient()
			return b.setProject, b.loadCmd,
				func(st loadStamp, mark string) {
					b.update(boardLoadedMsg{archived: true, stamp: st, tasks: []apiclient.Task{{ID: 1, Title: mark}}})
				},
				func() string { return firstTitle(b.tasks) }
		}},
		{name: "lanes", new: func() (func(projectSel) tea.Cmd, func() tea.Cmd, func(loadStamp, string), func() string) {
			b := newBoard()
			b.client = deadClient()
			b.lanes.expanded = expandSet{7}
			return b.setProject, func() tea.Cmd { return b.laneCmd(7) },
				func(st loadStamp, mark string) {
					b.update(boardLanesMsg{stamp: st, parentID: 7, lanes: []apiclient.Task{{ID: 8, Title: mark}}})
				},
				func() string { return firstTitle(b.lanes.lanesOf(7)) }
		}},
		{name: "issues", new: func() (func(projectSel) tea.Cmd, func() tea.Cmd, func(loadStamp, string), func() string) {
			v := newIssuesView()
			v.client = deadClient()
			return v.setProject, v.loadCmd,
				func(st loadStamp, mark string) {
					v.update(issuesLoadedMsg{state: v.state, stamp: st, issues: []apiclient.Issue{{ID: 1, Title: mark}}})
				},
				func() string {
					if len(v.issues) == 0 {
						return ""
					}
					return v.issues[0].Title
				}
		}},
		{name: "chats", new: func() (func(projectSel) tea.Cmd, func() tea.Cmd, func(loadStamp, string), func() string) {
			return chatsStamped(newChatsView(), false)
		}},
		{name: "archived chats", new: func() (func(projectSel) tea.Cmd, func() tea.Cmd, func(loadStamp, string), func() string) {
			return chatsStamped(newArchivedChatsView(), true)
		}},
		{name: "pull requests", new: func() (func(projectSel) tea.Cmd, func() tea.Cmd, func(loadStamp, string), func() string) {
			v := newPullRequestsView()
			v.client = deadClient()
			v.available = []githubProject{{project: apiclient.Project{ID: 1, Name: "a"}}}
			return v.setProject, v.loadCmd,
				func(st loadStamp, mark string) {
					v.update(prLoadedMsg{stamp: st, groups: []pullGroup{{project: apiclient.Project{Name: mark}}}})
				},
				func() string {
					if len(v.groups) == 0 {
						return ""
					}
					return v.groups[0].project.Name
				}
		}},
		{name: "workflows", new: func() (func(projectSel) tea.Cmd, func() tea.Cmd, func(loadStamp, string), func() string) {
			w := newWorkflowsView()
			w.client = deadClient()
			return w.setProject, w.loadCmd,
				func(st loadStamp, mark string) {
					w.update(workflowsLoadedMsg{stamp: st, blocks: []wfBlock{{name: mark}}})
				},
				func() string {
					if len(w.blocks) == 0 {
						return ""
					}
					return w.blocks[0].name
				}
		}},
		{name: "triggers", new: func() (func(projectSel) tea.Cmd, func() tea.Cmd, func(loadStamp, string), func() string) {
			v := newTriggersView()
			v.client = deadClient()
			return v.setProject, v.loadCmd,
				func(st loadStamp, mark string) {
					v.applyLoaded(triggersLoadedMsg{stamp: st, list: apiclient.TriggerList{Dir: mark}})
				},
				func() string { return v.list.Dir }
		}},
		{name: "projects", skipSel: true, new: func() (func(projectSel) tea.Cmd, func() tea.Cmd, func(loadStamp, string), func() string) {
			p := newProjectsView()
			p.client = deadClient()
			return nil, p.loadCmd,
				func(st loadStamp, mark string) {
					p.update(projectsLoadedMsg{stamp: st, projects: []apiclient.Project{{ID: 1, Name: mark}}})
				},
				func() string {
					if len(p.projects) == 0 {
						return ""
					}
					return p.projects[0].Name
				}
		}},
	}
}

func chatsStamped(v *chatsView, archived bool) (func(projectSel) tea.Cmd, func() tea.Cmd, func(loadStamp, string), func() string) {
	v.client = deadClient()
	return v.setProject, v.loadCmd,
		func(st loadStamp, mark string) {
			v.update(chatsLoadedMsg{archived: archived, stamp: st, chats: []apiclient.Chat{{ID: 1, Title: mark}}, names: map[int64]string{}})
		},
		func() string {
			if len(v.chats) == 0 {
				return ""
			}
			return v.chats[0].Title
		}
}

func firstTitle(tasks []apiclient.Task) string {
	if len(tasks) == 0 {
		return ""
	}
	return tasks[0].Title
}

// TestStampedViewsDropAPreviousProjectsLoad is the switch: each view reloads
// on setProject, and a response issued for the project it left is dropped
// whenever it lands.
func TestStampedViewsDropAPreviousProjectsLoad(t *testing.T) {
	const a, b = 1, 2
	for _, sv := range stampedViews() {
		if sv.skipSel {
			continue
		}
		t.Run(sv.name, func(t *testing.T) {
			setProject, _, deliver, shown := sv.new()
			if setProject(projectSel{id: a, name: "a"}) == nil {
				t.Fatal("setProject(A) returned no load")
			}
			stampA := loadStamp{project: a, seq: 1}
			if setProject(projectSel{id: b, name: "b"}) == nil {
				t.Fatal("setProject(B) returned no load")
			}
			if setProject(projectSel{id: b, name: "b renamed"}) != nil {
				t.Error("setProject to the same project reloaded")
			}
			// A's answer lands before B's: newer than anything applied, so
			// only the project half of the stamp can drop it.
			deliver(stampA, "A")
			if got := shown(); got != "" {
				t.Fatalf("A's answer after the switch to B painted %q", got)
			}
			deliver(loadStamp{project: b, seq: 2}, "B")
			deliver(stampA, "A")
			if got := shown(); got != "B" {
				t.Fatalf("after A's late answer the view shows %q, want B's rows", got)
			}
		})
	}
}

// TestStampedViewsKeepTheNewerLoad delivers two loads for the same project
// out of order: the newer one stays.
func TestStampedViewsKeepTheNewerLoad(t *testing.T) {
	for _, sv := range stampedViews() {
		t.Run(sv.name, func(t *testing.T) {
			_, load, deliver, shown := sv.new()
			for range 2 {
				if load() == nil {
					t.Fatal("load returned no command")
				}
			}
			deliver(loadStamp{seq: 2}, "new")
			deliver(loadStamp{seq: 1}, "old")
			if got := shown(); got != "new" {
				t.Fatalf("the view shows %q after the older load landed last, want new", got)
			}
		})
	}
}

// eventNote is one durable event as the stream delivers it.
func eventNote(typ string, project *int64) noteMsg {
	return noteMsg{note: apiclient.EventNote{Event: apiclient.Event{ID: 1, Type: typ, ProjectID: project}}}
}

// TestNilProjectEventsStillWakeTheirViews: the events the daemon does not
// attribute to a project pass the filter with a project selected.
func TestNilProjectEventsStillWakeTheirViews(t *testing.T) {
	sel := projectSel{id: 2, name: "b"}

	prs := newPullRequestsView()
	prs.client = deadClient()
	prs.setProject(sel)
	if _, cmd := prs.update(eventNote(eventTaskGitHubPullChanged, nil)); cmd == nil || !prs.refreshWait {
		t.Error("task.github_pull_changed did not refetch the pull requests")
	}

	b := newBoard()
	b.client = deadClient()
	b.setProject(sel)
	if _, cmd := b.update(eventNote(eventAgentQuotaChanged, nil)); cmd == nil {
		t.Error("agent.quota_changed did not refetch the board header")
	} else if !producesInfo(t, cmd) {
		t.Error("agent.quota_changed did not fetch /v1/info")
	}

	w := newWorkflowsView()
	w.client = deadClient()
	w.setProject(sel)
	if _, cmd := w.update(eventNote(eventWorkflowRegistryChanged, nil)); cmd == nil || !w.refreshPending {
		t.Error("workflow.registry_changed did not refetch the workflows")
	}
}

// producesInfo runs cmd (a dead client fails fast) and reports whether a
// boardInfoMsg came out of it.
func producesInfo(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	msg := runCmd(t, cmd, 15*time.Second)
	switch m := msg.(type) {
	case boardInfoMsg:
		return true
	case tea.BatchMsg:
		for _, c := range m {
			if c != nil && producesInfo(t, c) {
				return true
			}
		}
	}
	return false
}

// TestForeignEventsDoNotRefetch: each project-bearing view ignores an event
// for a project other than the selected one, and reacts to its own.
func TestForeignEventsDoNotRefetch(t *testing.T) {
	sel := projectSel{id: 2, name: "b"}
	cases := []struct {
		name    string
		typ     string
		view    func() panel
		pending func(panel) bool
	}{
		{
			"issues", "issue.created", func() panel { return scoped(newIssuesView(), sel) },
			func(p panel) bool { return p.(*issuesView).refreshWait },
		},
		{
			"chats", "chat.created", func() panel { return scoped(newChatsView(), sel) },
			func(p panel) bool { return p.(*chatsView).refreshPending },
		},
		{
			"pull requests", "task.state_changed", func() panel { return scoped(newPullRequestsView(), sel) },
			func(p panel) bool { return p.(*pullRequestsView).refreshWait },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := c.view()
			v.update(eventNote(c.typ, pid(1)))
			if c.pending(v) {
				t.Fatal("an event for another project opened a refetch window")
			}
			v.update(eventNote(c.typ, pid(sel.id)))
			if !c.pending(v) {
				t.Fatal("an event for the selected project did not open a refetch window")
			}
		})
	}
}

// TestBoardRefetchesForeignTaskEvents: the board's live listing is global
// (task 132 decision 17) and feeds the attention count, `!` and `H`, so a
// task event from a project other than the selected one still refetches it.
func TestBoardRefetchesForeignTaskEvents(t *testing.T) {
	sel := projectSel{id: 2, name: "b"}
	for _, typ := range []string{"task.state_changed", "task.created"} {
		b := scoped(newBoard(), sel)
		b.update(eventNote(typ, pid(1)))
		if !b.refreshPending {
			t.Errorf("a %s for another project did not refetch the board", typ)
		}
	}
}

// TestForeignListEventsStillRefetch: a project.* event is attributed to the
// project it describes, but it changes the project list every view still
// renders whole, so a foreign create, rename or delete refetches each view
// that reacts to it. trigger.* names its target project, and the triggers
// view lists every trigger until 132.11 scopes it.
func TestForeignListEventsStillRefetch(t *testing.T) {
	sel := projectSel{id: 2, name: "b"}
	cases := []struct {
		name    string
		typ     string
		view    func() panel
		pending func(panel) bool
	}{
		{
			"board project.deleted", "project.deleted", func() panel { return scoped(newBoard(), sel) },
			func(p panel) bool { return p.(*board).refreshPending },
		},
		{
			"issues project.deleted", "project.deleted", func() panel { return scoped(newIssuesView(), sel) },
			func(p panel) bool { return p.(*issuesView).refreshWait },
		},
		{
			"pull requests project.updated", "project.updated", func() panel { return scoped(newPullRequestsView(), sel) },
			func(p panel) bool { return p.(*pullRequestsView).refreshWait },
		},
		{
			"workflows project.created", "project.created", func() panel { return scoped(newWorkflowsView(), sel) },
			func(p panel) bool { return p.(*workflowsView).refreshPending },
		},
		{
			"workflows project.updated", "project.updated", func() panel { return scoped(newWorkflowsView(), sel) },
			func(p panel) bool { return p.(*workflowsView).refreshPending },
		},
		{
			"triggers project.updated", "project.updated", func() panel { return scoped(newTriggersView(), sel) },
			func(p panel) bool { return p.(*triggersView).refreshPending },
		},
		{
			"triggers trigger.fired", "trigger.fired", func() panel { return scoped(newTriggersView(), sel) },
			func(p panel) bool { return p.(*triggersView).refreshPending },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := c.view()
			v.update(eventNote(c.typ, pid(1)))
			if !c.pending(v) {
				t.Fatalf("a foreign %s did not refetch", c.typ)
			}
		})
	}
}

// scoped connects v to a dead client and selects sel on it.
func scoped[V interface {
	panel
	clientAware
	projectScoped
}](v V, sel projectSel) V {
	v.setClient(deadClient())
	v.setProject(sel)
	return v
}

// TestChatsDebounceABurst: a turn's burst of chat.* events is one fetch.
func TestChatsDebounceABurst(t *testing.T) {
	v := newChatsView()
	v.client = deadClient()
	var armed int
	for range 5 {
		if _, cmd := v.update(eventNote("chat.state_changed", nil)); cmd != nil {
			armed++
		}
	}
	if armed != 1 {
		t.Fatalf("five chat events armed %d refetch windows, want 1", armed)
	}
	_, cmd := v.update(chatsRefreshMsg{})
	if cmd == nil {
		t.Fatal("the closing window did not load")
	}
	if v.refreshPending {
		t.Fatal("the window stayed open after it closed")
	}
	// The archived board's window is its own.
	if _, cmd := v.update(chatsRefreshMsg{archived: true}); cmd != nil {
		t.Error("the live board took the archived board's refresh")
	}
}
