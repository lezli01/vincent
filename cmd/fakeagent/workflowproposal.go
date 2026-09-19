package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// stageWorkflows is the stage-workflows scenario: what a global
// update-workflows run's agent does (task 123), so a gate can drive the real
// built-in on every platform. It lists the global scope with the real
// `vincent workflow ls --global --json`, clears its staging directory as the
// prompt says, and stages every valid file with FAKEAGENT_PROPOSAL_LINE
// appended — or, when that is unset, an empty manifest, the "nothing needs a
// change" proposal. The staging directory is VINCENT_DATA_DIR joined with
// workflow-proposals and VINCENT_TASK_ID: the gate's daemon runs with an
// explicit data dir, which its agents inherit, so asking doctor for it would
// only add a probe. It returns the line the result text carries.
func stageWorkflows() (string, error) {
	bin := os.Getenv("FAKEAGENT_VINCENT_BIN")
	data := os.Getenv("VINCENT_DATA_DIR")
	task := os.Getenv("VINCENT_TASK_ID")
	if bin == "" || data == "" || task == "" {
		return "", errors.New("stage-workflows needs FAKEAGENT_VINCENT_BIN, VINCENT_DATA_DIR and VINCENT_TASK_ID")
	}
	if _, err := strconv.ParseInt(task, 10, 64); err != nil {
		return "", fmt.Errorf("VINCENT_TASK_ID %q: %w", task, err)
	}
	// Exit 1 is "no global workflows", which the inventory step already
	// stopped on; anything it printed is still the listing.
	out, err := exec.Command(bin, "workflow", "ls", "--global", "--json").Output()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return "", fmt.Errorf("workflow ls --global: %w", err)
	}
	var listed []struct {
		File    string `json:"file"`
		Version string `json:"version"`
		Valid   bool   `json:"valid"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		return "", fmt.Errorf("workflow ls --global --json: %w: %q", err, out)
	}

	staging := filepath.Join(data, "workflow-proposals", task)
	if err := os.RemoveAll(staging); err != nil {
		return "", err
	}
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return "", err
	}
	manifest := map[string]string{}
	if line := os.Getenv("FAKEAGENT_PROPOSAL_LINE"); line != "" {
		for _, l := range listed {
			if !l.Valid {
				continue
			}
			src, err := os.ReadFile(l.File)
			if err != nil {
				return "", err
			}
			base := filepath.Base(l.File)
			if err := os.WriteFile(filepath.Join(staging, base), append(src, []byte(line+"\n")...), 0o600); err != nil {
				return "", err
			}
			manifest[base] = l.Version
		}
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(staging, "manifest.json"), raw, 0o600); err != nil {
		return "", err
	}
	return fmt.Sprintf("PROPOSAL: staged %d workflow file(s) in %s", len(manifest), staging), nil
}
