package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// Over SSH the font would land on the wrong machine, so nothing is fetched.
func TestFontsInstallRefusesOverSSH(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "1 2 3 4")
	cmd := newFontsCmd()
	var stderr bytes.Buffer
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"install"})
	var exit exitError
	if err := cmd.Execute(); !errors.As(err, &exit) || exit.code != 1 {
		t.Fatalf("install over SSH answered %v", err)
	}
	if !strings.Contains(stderr.String(), "SSH") {
		t.Errorf("the refusal does not say why: %q", stderr.String())
	}
}
