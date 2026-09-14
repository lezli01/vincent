package tui

import (
	"context"
	"encoding/json"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The two dry runs (§15 view 11, task 096 decision 31H). Both fire nothing.
//
// `T` judges a sample event somebody writes: POST /v1/triggers/{id}/test runs
// it through the real match, if, dedupe and render, and writes nothing. The
// sample lives in triggersView.samples for the session — reopening `T` on the
// same trigger brings it back — and is never written to disk (decision 27).
//
// `X` runs the source once for real: POST /v1/triggers/{id}/poll judges what
// it returns with no fire, no cursor advance, no ledger row and no poll-health
// change. It is a layer rather than a line on the list, so nothing it shows is
// mistaken for what the poller did.

type (
	triggerTestedMsg struct {
		id        string
		judgement apiclient.TriggerJudgement
		err       error
	}
	triggerPolledMsg struct {
		id   string
		poll apiclient.TriggerDryPoll
		err  error
	}
)

// trigPollTimeout bounds a live poll from the client's side. The daemon bounds
// the command itself; this only keeps a hung connection from holding the
// layer open for ever.
const trigPollTimeout = 2 * time.Minute

// trigSampleSeed is the event a first `T` opens on: the one key every event
// needs (appendix A).
const trigSampleSeed = "{\n  \"id\": \"sample-1\"\n}"

type trigDryRun struct {
	id   string
	poll bool
	// pane is the sample event, in test mode only.
	pane      textPane
	running   bool
	judgement *apiclient.TriggerJudgement
	result    *apiclient.TriggerDryPoll
	err       string
}

func (v *triggersView) openTest() tea.Cmd {
	s, ok := v.current()
	if !ok {
		return nil
	}
	sample, kept := v.samples[s.ID]
	if !kept {
		sample = trigSampleSeed
	}
	d := &trigDryRun{id: s.ID, pane: newTextPane()}
	d.pane.SetValue(sample)
	v.err, v.note = "", ""
	v.dry = d
	return d.pane.Focus()
}

func (v *triggersView) openPoll() tea.Cmd {
	s, ok := v.current()
	if !ok {
		return nil
	}
	v.err, v.note = "", ""
	v.dry = &trigDryRun{id: s.ID, poll: true}
	return v.pollCmd()
}

func (v *triggersView) updateDryKey(msg tea.KeyPressMsg) tea.Cmd {
	d := v.dry
	switch msg.String() {
	case "esc":
		if !d.poll {
			v.samples[d.id] = d.pane.Value()
		}
		v.dry = nil
		return nil
	case "ctrl+s":
		if d.poll {
			return v.pollCmd()
		}
		return v.testCmd()
	}
	if d.poll {
		return nil
	}
	pane, cmd := d.pane.Update(msg)
	d.pane = pane
	return cmd
}

func (v *triggersView) testCmd() tea.Cmd {
	d := v.dry
	text := d.pane.Value()
	v.samples[d.id] = text
	var event map[string]any
	if err := json.Unmarshal([]byte(text), &event); err != nil || event == nil {
		d.err = "the sample must be one JSON object"
		if err != nil {
			d.err += ": " + err.Error()
		}
		d.judgement = nil
		return nil
	}
	client := v.client
	if client == nil {
		d.err = "not connected"
		return nil
	}
	d.running, d.err = true, ""
	id := d.id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		j, err := client.TestTrigger(ctx, id, event)
		return triggerTestedMsg{id: id, judgement: j, err: err}
	}
}

func (v *triggersView) pollCmd() tea.Cmd {
	d := v.dry
	client := v.client
	if client == nil {
		d.err = "not connected"
		return nil
	}
	d.running, d.err, d.result = true, "", nil
	id := d.id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), trigPollTimeout)
		defer cancel()
		res, err := client.PollTrigger(ctx, id)
		return triggerPolledMsg{id: id, poll: res, err: err}
	}
}
