package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// focusProbe is a view that records the focus reports it hears.
type focusProbe struct {
	panel
	heard []tea.Msg
}

func (p *focusProbe) update(msg tea.Msg) (panel, tea.Cmd) {
	switch msg.(type) {
	case tea.FocusMsg, tea.BlurMsg:
		p.heard = append(p.heard, msg)
	}
	return p, nil
}

// TestFocusReportsReachEveryView: the root broadcasts focus and blur to
// every view, the inactive ones included (task 156) — the chat workspace
// decides from them whether a finished turn was seen, and it is not always
// the active view when one arrives.
func TestFocusReportsReachEveryView(t *testing.T) {
	m := attentionRoot(t)
	m.active = viewHome
	probes := map[viewID]*focusProbe{}
	for _, id := range []viewID{viewHome, viewChat, viewChats} {
		p := &focusProbe{panel: m.views[id]}
		probes[id] = p
		m.views[id] = p
	}
	m.Update(tea.BlurMsg{})
	m.Update(tea.FocusMsg{})
	for id, p := range probes {
		if len(p.heard) != 2 {
			t.Errorf("view %v heard %d focus reports, want 2", id, len(p.heard))
			continue
		}
		if _, ok := p.heard[0].(tea.BlurMsg); !ok {
			t.Errorf("view %v heard %T first, want the blur", id, p.heard[0])
		}
	}
	if !m.focused {
		t.Error("the root is unfocused after a focus report")
	}
}
