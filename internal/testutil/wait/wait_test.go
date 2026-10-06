package wait

import (
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
)

func TestScale(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want float64
	}{
		{"", 1},
		{"3", 3},
		{"1.5", 1.5},
		{"0", 1},
		{"-2", 1},
		{"nope", 1},
		{"+Inf", 1},
	} {
		t.Setenv(EnvScale, tc.env)
		if got := Scale(); got != tc.want {
			t.Errorf("Scale() with %q = %v, want %v", tc.env, got, tc.want)
		}
		if got, want := Timeout(time.Second), time.Duration(float64(time.Second)*tc.want); got != want {
			t.Errorf("Timeout(1s) with %q = %v, want %v", tc.env, got, want)
		}
		// The production copy must read the factor the same way, or the
		// client and daemon-start budgets drift from every wait here.
		if got, want := apiclient.ScaleTimeout(time.Second), Timeout(time.Second); got != want {
			t.Errorf("apiclient.ScaleTimeout(1s) with %q = %v, want %v", tc.env, got, want)
		}
	}
	if EnvScale != apiclient.EnvTimeoutScale {
		t.Errorf("EnvScale = %q, apiclient.EnvTimeoutScale = %q", EnvScale, apiclient.EnvTimeoutScale)
	}
}

func TestUntilReturnsOnceCondHolds(t *testing.T) {
	n := 0
	Until(t, "the third call", func() bool { n++; return n == 3 })
	if n != 3 {
		t.Errorf("cond called %d times, want 3", n)
	}
}
