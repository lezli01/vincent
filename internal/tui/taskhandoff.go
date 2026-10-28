package tui

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The task workspace Overview's Items block (§15 view 2, task 137.11): on a
// handoff's task, each claimed issue with its delivery kind and outcome. The
// task DTO's handoff_id names the handoff; one GET /v1/handoffs/{id} reads
// the items, and handoff.item_changed and handoff.settled on the task's own
// stream re-read them.

// handoffItemsMsg is GetHandoff's answer for the Overview.
type handoffItemsMsg struct {
	id      int64
	handoff apiclient.Handoff
	err     error
}

// handoffFetch is the Overview's read of the task's handoff.
type handoffFetch struct {
	id      int64
	loading bool
	loaded  bool
	handoff apiclient.Handoff
	titles  map[int64]string
	err     error
}

func handoffItemsCmd(client *apiclient.Client, id int64) tea.Cmd {
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		h, err := client.GetHandoff(ctx, id)
		return handoffItemsMsg{id: id, handoff: h, err: err}
	}
}

// syncHandoff reads the task's handoff once per handoff id.
func (t *taskView) syncHandoff() tea.Cmd {
	hid := t.detail.task.HandoffID
	if !t.detail.loaded || hid == nil {
		return nil
	}
	if t.handoff.id == *hid && (t.handoff.loading || t.handoff.loaded) {
		return nil
	}
	t.handoff = handoffFetch{id: *hid, loading: true}
	return handoffItemsCmd(t.detail.client, *hid)
}

// applyHandoffItems lands a read for the handoff still on screen.
func (t *taskView) applyHandoffItems(msg handoffItemsMsg) {
	if msg.id != t.handoff.id {
		return
	}
	t.handoff.loading, t.handoff.loaded = false, true
	t.handoff.handoff, t.handoff.err = msg.handoff, msg.err
	t.handoff.titles = handoffFileTitles(msg.handoff.File)
}

// handoffNoteCmd re-reads the items on a handoff event on this task's
// stream: the item_changed and settled events carry the handoff's task.
func (t *taskView) handoffNoteCmd(ev apiclient.Event) tea.Cmd {
	if t.handoff.id == 0 || ev.TaskID == nil || *ev.TaskID != t.detail.taskID {
		return nil
	}
	switch ev.Type {
	case "handoff.item_changed", "handoff.settled":
		t.handoff.loading = true
		return handoffItemsCmd(t.detail.client, t.handoff.id)
	}
	return nil
}

// handoffFileTitles reads each item's issue title from the handoff file:
// the item rows carry no title of their own.
func handoffFileTitles(raw json.RawMessage) map[int64]string {
	var f struct {
		Items []struct {
			Issue struct {
				ID    *int64 `json:"id"`
				Title string `json:"title"`
			} `json:"issue"`
		} `json:"items"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &f) != nil {
		return nil
	}
	out := map[int64]string{}
	for _, it := range f.Items {
		if it.Issue.ID != nil {
			out[*it.Issue.ID] = it.Issue.Title
		}
	}
	return out
}

// handoffItemLines is the Items block, nil on a task that is no handoff's.
func (t *taskView) handoffItemLines(width int) []string {
	if t.detail.task.HandoffID == nil {
		return nil
	}
	f := t.handoff
	head := " " + styleTitle.Render("Items") + styleDim.Render("  "+handoffRef(*t.detail.task.HandoffID))
	switch {
	case f.err != nil:
		return []string{"", head, styleBad.Render("  could not read the handoff: " + errString(f.err))}
	case !f.loaded:
		return []string{"", head, styleDim.Render("  reading…")}
	}
	head += styleDim.Render(" · " + f.handoff.State)
	out := []string{"", head}
	for _, it := range f.handoff.Items {
		kind := "none"
		if it.Delivery != nil && it.Delivery.Kind != "" {
			kind = it.Delivery.Kind
		}
		outcome := it.Outcome
		if outcome == "" {
			outcome = "unresolved"
		}
		lead := "  " + styleKey.Render(padRight("#"+strconv.FormatInt(it.IssueID, 10), 6)) + " "
		tail := "  " + styleDim.Render(kind+" · ") + outcome
		title := fitTitle(f.titles[it.IssueID], width-cols(lead)-cols(tail))
		out = append(out, strings.TrimRight(lead+title+tail, " "))
	}
	return out
}
