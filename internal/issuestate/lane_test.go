package issuestate

import (
	"reflect"
	"testing"
)

func TestLaneOf(t *testing.T) {
	for _, tc := range []struct {
		s                          State
		unsettled, finished, ready bool
		want                       Lane
	}{
		{Open, false, false, false, LaneOpen},
		{Open, false, true, true, LaneHandOff},
		// Finished but not ready, or only a side task finished: idle.
		{Open, false, true, false, LaneInProgress},
		{Open, true, false, false, LaneInProgress},
		{Open, true, true, false, LaneInProgress},
		{Open, true, true, true, LaneInProgress},
		{Closed, false, false, false, LaneDone},
		{Closed, false, true, true, LaneDone},
		{Closed, false, true, false, LaneDone},
		{Closed, true, false, false, LaneDone},
		{Closed, true, true, true, LaneDone},
		// An unknown stored state reads as open.
		{"waiting", false, false, false, LaneOpen},
		{"waiting", true, false, false, LaneInProgress},
		{"waiting", false, true, true, LaneHandOff},
		{"waiting", false, true, false, LaneInProgress},
	} {
		if got := LaneOf(tc.s, tc.unsettled, tc.finished, tc.ready, false); got != tc.want {
			t.Errorf("LaneOf(%s, unsettled=%v, finished=%v, ready=%v) = %s, want %s",
				tc.s, tc.unsettled, tc.finished, tc.ready, got, tc.want)
		}
	}
}

func TestLanesInBoardOrder(t *testing.T) {
	want := []Lane{"open", "in_progress", "hand_off", "done"}
	if !reflect.DeepEqual(Lanes, want) {
		t.Errorf("Lanes = %v, want %v", Lanes, want)
	}
	for _, l := range Lanes {
		if !ValidLane(string(l)) {
			t.Errorf("ValidLane(%s) = false", l)
		}
	}
	for _, s := range []string{"", "Open", "closed", "handoff"} {
		if ValidLane(s) {
			t.Errorf("ValidLane(%q) = true", s)
		}
	}
}
