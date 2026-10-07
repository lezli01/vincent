package issuestate

import (
	"reflect"
	"testing"
)

func TestLaneOf(t *testing.T) {
	for _, tc := range []struct {
		s                 State
		unsettled, isDone bool
		want              Lane
	}{
		{Open, false, false, LaneOpen},
		{Open, false, true, LaneHandOff},
		{Open, true, false, LaneInProgress},
		{Open, true, true, LaneInProgress},
		{Closed, false, false, LaneDone},
		{Closed, false, true, LaneDone},
		{Closed, true, false, LaneDone},
		{Closed, true, true, LaneDone},
		// An unknown stored state reads as open.
		{"waiting", false, false, LaneOpen},
		{"waiting", true, false, LaneInProgress},
		{"waiting", false, true, LaneHandOff},
	} {
		if got := LaneOf(tc.s, tc.unsettled, tc.isDone); got != tc.want {
			t.Errorf("LaneOf(%s, unsettled=%v, done=%v) = %s, want %s", tc.s, tc.unsettled, tc.isDone, got, tc.want)
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
