package api

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/mcp"
)

// TestMCPToolBodyHintsMatchRequestStructs keeps every `Body:` hint in the MCP
// tool descriptions honest: JSON decoding is DisallowUnknownFields API-wide,
// so a hint that names a key the handler does not decode turns a call that
// follows the description into a 400.
func TestMCPToolBodyHintsMatchRequestStructs(t *testing.T) {
	t.Parallel()

	structs := map[string]reflect.Type{
		"project_create":        reflect.TypeOf(projectCreateRequest{}),
		"workflow_validate":     reflect.TypeOf(validateRequest{}),
		"trigger_validate":      reflect.TypeOf(triggerValidateRequest{}),
		"trigger_test":          reflect.TypeOf(triggerTestRequest{}),
		"task_create":           reflect.TypeOf(taskCreateRequest{}),
		"task_retry":            reflect.TypeOf(retryRequest{}),
		"task_repair":           reflect.TypeOf(repairRequest{}),
		"task_answer":           reflect.TypeOf(answerRequest{}),
		"task_follow_up":        reflect.TypeOf(followUpRequest{}),
		"step_status":           reflect.TypeOf(stepStatusRequest{}),
		"task_github_pull_link": reflect.TypeOf(githubPullLinkRequest{}),
	}

	// exempt: the tool names a body in its description, but its handler never
	// decodes one — handleTaskReject goes through runAction, which takes no
	// request struct. Left as its own follow-up rather than changed here.
	exempt := map[string]string{
		"task_reject": "handleTaskReject goes through runAction, which decodes no body",
	}

	// The hint marker is "Body:", not "Body: {": task_follow_up's hint reads
	// "Body: exactly one of {prompt, run, workflow}, ...", so gating on the
	// brace would miss it. The {…} groups are extracted below regardless.
	const marker = "Body:"
	braces := regexp.MustCompile(`\{([^{}]*)\}`)

	tagsOf := func(typ reflect.Type) map[string]bool {
		tags := make(map[string]bool, typ.NumField())
		for i := 0; i < typ.NumField(); i++ {
			name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			tags[name] = true
		}
		return tags
	}

	hinted := map[string]bool{}
	for _, r := range mcp.Routes() {
		if !strings.Contains(r.Description, marker) {
			continue
		}
		hinted[r.Tool] = true
		typ, ok := structs[r.Tool]
		if !ok {
			if _, isExempt := exempt[r.Tool]; !isExempt {
				t.Errorf("tool %q names a body but has no request struct in this test: add it to structs, or to exempt with a reason", r.Tool)
			}
			continue
		}
		tags := tagsOf(typ)
		for _, m := range braces.FindAllStringSubmatch(r.Description, -1) {
			for _, part := range strings.FieldsFunc(m[1], func(c rune) bool { return c == ',' || c == '|' }) {
				key := strings.TrimSpace(part)
				key = strings.TrimSuffix(key, "?")
				key = strings.TrimSuffix(key, "...")
				if key == "" {
					continue
				}
				if !tags[key] {
					t.Errorf("tool %q names body key %q, but %s decodes no such field (no %q json tag): fix the description in internal/mcp/tools.go — the handler's struct is the contract, the description is what changes",
						r.Tool, key, typ, key)
				}
			}
		}
	}

	for tool := range structs {
		if !hinted[tool] {
			t.Errorf("stale structs entry %q: no tool description names a body for it anymore — remove the entry, or fix the description in internal/mcp/tools.go", tool)
		}
	}
	for tool := range exempt {
		if !hinted[tool] {
			t.Errorf("stale exempt entry %q: no tool description names a body for it anymore — remove the entry, or fix the description in internal/mcp/tools.go", tool)
		}
	}
}
