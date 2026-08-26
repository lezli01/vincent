package procx

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestIdentityIsStableAcrossReads(t *testing.T) {
	cmd, proc := startSleeper(t)
	defer func() {
		_ = proc.Kill()
		_ = cmd.Wait()
		proc.Release()
	}()

	first, err := Identity(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	if first == "" {
		t.Fatal("Identity returned an empty token")
	}
	if !strings.HasPrefix(first, identityScheme+":") {
		t.Errorf("Identity = %q, want the %q scheme prefix", first, identityScheme)
	}
	// The whole guard rests on this: the same process must answer with the
	// same bytes at spawn and at recovery, an arbitrary interval later.
	time.Sleep(50 * time.Millisecond)
	second, err := Identity(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("second Identity: %v", err)
	}
	if second != first {
		t.Errorf("Identity of one process changed: %q then %q", first, second)
	}
}

func TestIdentityDiffersBetweenLiveProcesses(t *testing.T) {
	cmdA, procA := startSleeper(t)
	cmdB, procB := startSleeper(t)
	defer func() {
		_ = procA.Kill()
		_ = cmdA.Wait()
		procA.Release()
		_ = procB.Kill()
		_ = cmdB.Wait()
		procB.Release()
	}()

	a, err := Identity(cmdA.Process.Pid)
	if err != nil {
		t.Fatalf("Identity(a): %v", err)
	}
	b, err := Identity(cmdB.Process.Pid)
	if err != nil {
		t.Fatalf("Identity(b): %v", err)
	}
	// Two processes alive at once cannot be each other, whatever the
	// platform's timestamp resolution says.
	if a == b {
		t.Errorf("two live processes share an identity: %q", a)
	}
}

func TestIdentitySelf(t *testing.T) {
	got, err := Identity(os.Getpid())
	if err != nil {
		t.Fatalf("Identity(self): %v", err)
	}
	if !strings.HasPrefix(got, identityScheme+":") || len(got) <= len(identityScheme)+1 {
		t.Errorf("Identity(self) = %q, want a %q-prefixed token with a body", got, identityScheme)
	}
}

func TestIdentityGoneAfterExit(t *testing.T) {
	cmd, proc := startSleeper(t)
	if err := proc.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	_ = cmd.Wait()
	proc.Release()

	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := Identity(cmd.Process.Pid)
		if errors.Is(err, ErrProcessGone) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Identity after exit = %v, want ErrProcessGone", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
