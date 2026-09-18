package apiclient_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/trigger"
)

// The trigger client's types are a hand-written mirror of the server's DTOs
// and of internal/trigger's judgement types, so the only test that proves
// anything decodes what the real handlers encode.

const triggerClientSentinel = "trigger-client-helper"

// TestTriggerClientHelperProcess is not a test: it is a trigger's poll
// command, this test binary re-executed, so no shell is involved on any
// platform.
func TestTriggerClientHelperProcess(t *testing.T) {
	i := slices.Index(os.Args, triggerClientSentinel)
	if i < 0 {
		t.Skip("not running as a trigger's poll command")
	}
	for _, l := range os.Args[i+1:] {
		fmt.Println(l)
	}
	os.Exit(0)
}

type triggerClientHarness struct {
	c         *apiclient.Client
	st        *store.Store
	projectID int64
}

func newTriggerClientHarness(t *testing.T) *triggerClientHarness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	project := &store.Project{Name: "live", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(t.Context(), project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "triggers")
	reg := trigger.NewRegistry(dir, nil)
	reg.Reload()
	var srv atomic.Pointer[api.Server]
	mgr := trigger.NewManager(trigger.Deps{
		Store: st,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			srv.Load().Inner().ServeHTTP(w, r)
		}),
		Registry: reg,
	})
	s := api.New(api.Deps{
		Token:           testToken,
		Config:          config.Default,
		StartedAt:       time.Now(),
		ListenAddr:      "127.0.0.1:0",
		RequestStop:     func() {},
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:           st,
		Triggers:        mgr,
		TriggerRegistry: reg,
		TriggerWriter:   trigger.NewWriter(dir),
	})
	srv.Store(s)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &triggerClientHarness{c: apiclient.New(ts.URL, testToken), st: st, projectID: project.ID}
}

func apiStatus(t *testing.T, err error, status int) *apiclient.Error {
	t.Helper()
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) || apiErr.Status != status {
		t.Fatalf("err = %v, want an http %d apiclient.Error", err, status)
	}
	return apiErr
}

func TestTriggerWritesOverTheWire(t *testing.T) {
	h := newTriggerClientHarness(t)
	ctx := t.Context()

	created, err := h.c.CreateTrigger(ctx, apiclient.CreateTriggerRequest{
		ID: "live", ProjectID: h.projectID, PollInterval: "10m", Title: "Live {{ .Event.title }}",
		Command: []string{"poll-command"},
	})
	if err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	if created.ID != "live" || filepath.Base(created.File) != "live.yaml" || created.Version == "" || created.Errors == nil {
		t.Errorf("create result = %+v", created)
	}
	_, err = h.c.CreateTrigger(ctx, apiclient.CreateTriggerRequest{ID: "live", ProjectID: h.projectID})
	_ = apiStatus(t, err, http.StatusConflict)

	list, err := h.c.Triggers(ctx)
	if err != nil {
		t.Fatalf("Triggers: %v", err)
	}
	if list.Enabled || list.Dir == "" || len(list.Triggers) != 1 {
		t.Fatalf("list = %+v", list)
	}
	row := list.Triggers[0]
	if row.ID != "live" || row.Version != created.Version || !row.Valid || row.Errors == nil || row.Enabled ||
		row.Armed || row.DisarmedReason == "" || row.SourceType != "command" || row.ActionType != "create_task" ||
		row.ProjectID != h.projectID || row.OnFire != "propose" || row.Permission != "restricted" || row.Poll.Seeded {
		t.Errorf("summary = %+v", row)
	}

	detail, err := h.c.Trigger(ctx, "live")
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	source, _ := detail.Definition["source"].(map[string]any)
	if detail.Source == "" || detail.Definition["id"] != "live" || source["poll_interval"] != "10m" {
		t.Errorf("detail = %+v, want the file and its generic definition", detail)
	}

	patched, err := h.c.PatchTrigger(ctx, "live", created.Version,
		[]apiclient.WorkflowOp{{Op: "set", Path: "source.poll_interval", Value: "15m"}})
	if err != nil {
		t.Fatalf("PatchTrigger: %v", err)
	}
	if patched.Version == "" || patched.Version == created.Version {
		t.Errorf("patched version = %q, want a new token", patched.Version)
	}
	_, err = h.c.PatchTrigger(ctx, "live", created.Version,
		[]apiclient.WorkflowOp{{Op: "set", Path: "source.poll_interval", Value: "20m"}})
	if e := apiStatus(t, err, http.StatusConflict); e.Code != "invalid_state" || e.Details["version"] != patched.Version {
		t.Errorf("stale patch = %+v, want details.version %q", e, patched.Version)
	}
	_, err = h.c.PatchTrigger(ctx, "live", patched.Version,
		[]apiclient.WorkflowOp{{Op: "set", Path: "source.poll_interval", Value: "1ms"}})
	if e := apiStatus(t, err, http.StatusBadRequest); !strings.Contains(e.Details["errors"], "source.poll_interval") {
		t.Errorf("invalid patch = %+v, want the finding in details.errors", e)
	}

	if err := h.c.DeleteTrigger(ctx, "live", created.Version); err == nil {
		t.Fatal("DeleteTrigger with a stale version succeeded")
	} else if e := apiStatus(t, err, http.StatusConflict); e.Details["version"] != patched.Version {
		t.Errorf("stale delete = %+v", e)
	}
	if err := h.c.DeleteTrigger(ctx, "live", patched.Version); err != nil {
		t.Fatalf("DeleteTrigger: %v", err)
	}
	_, err = h.c.Trigger(ctx, "live")
	_ = apiStatus(t, err, http.StatusNotFound)
}

func TestTriggerReadsAndDryRunsOverTheWire(t *testing.T) {
	h := newTriggerClientHarness(t)
	ctx := t.Context()

	// The served schema decodes without losing a field: re-encoding the
	// client's value gives back the descriptor.
	schema, err := h.c.TriggerSchema(ctx)
	if err != nil {
		t.Fatalf("TriggerSchema: %v", err)
	}
	got, _ := json.Marshal(schema)
	want, _ := json.Marshal(trigger.SchemaDescriptor())
	var gotAny, wantAny any
	_ = json.Unmarshal(got, &gotAny)
	_ = json.Unmarshal(want, &wantAny)
	if !reflect.DeepEqual(gotAny, wantAny) {
		t.Errorf("apiclient.TriggerSchema drops or renames a field:\n got %s\nwant %s", got, want)
	}

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	if _, err := h.c.CreateTrigger(ctx, apiclient.CreateTriggerRequest{
		ID: "live", ProjectID: h.projectID, Title: "Live {{ .Event.title }}",
		Command: []string{
			self, "-test.run=TestTriggerClientHelperProcess", triggerClientSentinel,
			`{"id":"e1","title":"One"}`, `{"cursor":"c1"}`,
		},
	}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	detail, err := h.c.Trigger(ctx, "live")
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	v, err := h.c.ValidateTrigger(ctx, detail.Source, "live")
	if err != nil || !v.Valid || v.Errors == nil || len(v.Errors) != 0 {
		t.Errorf("ValidateTrigger(valid) = %+v, %v", v, err)
	}
	v, err = h.c.ValidateTrigger(ctx, detail.Source, "other")
	if err != nil || v.Valid || len(v.Errors) == 0 || v.Errors[0].Path != "id" {
		t.Errorf("ValidateTrigger(wrong stem) = %+v, %v", v, err)
	}

	j, err := h.c.TestTrigger(ctx, "live", map[string]any{"id": "e9", "title": "Nine"})
	if err != nil {
		t.Fatalf("TestTrigger: %v", err)
	}
	if j.EventID != "e9" || !j.Matched || j.DedupeKey != "e9" || j.WouldDedupe || j.Outcome != apiclient.TriggerFired ||
		j.Action == nil || j.Action.Type != "create_task" || j.Action.Method != http.MethodPost || j.Action.Path != "/v1/tasks" {
		t.Fatalf("judgement = %+v", j)
	}
	var body map[string]any
	if err := json.Unmarshal(j.Action.Body, &body); err != nil || body["title"] != "Live Nine" || body["paused"] != true {
		t.Errorf("action body = %s (%v)", j.Action.Body, err)
	}

	dp, err := h.c.PollTrigger(ctx, "live")
	if err != nil {
		t.Fatalf("PollTrigger: %v", err)
	}
	if !dp.Seed || dp.Error != "" || len(dp.Events) != 1 || dp.Events[0].EventID != "e1" ||
		dp.Events[0].Outcome != apiclient.TriggerFired || dp.Cursor == nil || *dp.Cursor != "c1" {
		t.Errorf("dry poll = %+v", dp)
	}

	older, newer := time.Now().Add(-time.Minute), time.Now()
	for _, d := range []*store.TriggerDelivery{
		{TriggerID: "live", EventID: "a", DedupeKey: "a", Outcome: store.DeliverySeeded, CreatedAt: older},
		{TriggerID: "live", EventID: "b", DedupeKey: "key-b", Outcome: store.DeliveryRefused, Detail: "no task", CreatedAt: newer},
	} {
		if _, err := h.st.RecordTriggerDelivery(ctx, d); err != nil {
			t.Fatalf("record delivery: %v", err)
		}
	}
	rows, err := h.c.TriggerDeliveries(ctx, "live", 0)
	if err != nil {
		t.Fatalf("TriggerDeliveries: %v", err)
	}
	if len(rows) != 2 || rows[0].EventID != "b" || rows[1].EventID != "a" {
		t.Fatalf("deliveries = %+v, want b then a", rows)
	}
	if r := rows[0]; r.ID == 0 || r.TriggerID != "live" || r.DedupeKey != "key-b" || r.Outcome != apiclient.TriggerRefused ||
		r.Detail != "no task" || r.TaskID != nil || r.CreatedAt == "" {
		t.Errorf("delivery = %+v", r)
	}
	if rows[1].Outcome != apiclient.TriggerSeeded {
		t.Errorf("seed row outcome = %q, want seeded", rows[1].Outcome)
	}
	if rows, err := h.c.TriggerDeliveries(ctx, "live", 1); err != nil || len(rows) != 1 {
		t.Errorf("TriggerDeliveries(limit 1) = %+v, %v", rows, err)
	}
}

// TestScheduleTriggerOverTheWire: the fifth source type reaches the wire with
// no new route and no new client field (task 121). The starter writes a
// command source, and one PATCH turns it into a clock — which is the path a
// form takes too, since it renders from the served descriptor.
func TestScheduleTriggerOverTheWire(t *testing.T) {
	h := newTriggerClientHarness(t)
	ctx := t.Context()

	created, err := h.c.CreateTrigger(ctx, apiclient.CreateTriggerRequest{
		ID: "nightly", ProjectID: h.projectID, PollInterval: "10m",
		Command: []string{"poll-command"}, Title: "Sweep {{ .Event.scheduled_at }}",
	})
	if err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	patched, err := h.c.PatchTrigger(ctx, "nightly", created.Version, []apiclient.WorkflowOp{
		{Op: "set", Path: "source.type", Value: "schedule"},
		{Op: "set", Path: "source.every", Value: "2s"},
		{Op: "remove", Path: "source.poll_interval"},
		{Op: "remove", Path: "source.command"},
	})
	if err != nil {
		t.Fatalf("PatchTrigger to a schedule: %v", err)
	}
	if len(patched.Errors) != 0 {
		t.Fatalf("the schedule did not validate: %+v", patched.Errors)
	}

	list, err := h.c.Triggers(ctx)
	if err != nil {
		t.Fatalf("Triggers: %v", err)
	}
	row := list.Triggers[0]
	if row.SourceType != "schedule" || !row.Valid || row.Enabled || row.Poll.Seeded || row.OnFire != "propose" {
		t.Errorf("summary = %+v, want a valid, disabled, unanchored schedule", row)
	}

	// There is no source to run once, so the live-poll dry run refuses; the
	// supplied-event dry run is unchanged and still judges.
	if _, err := h.c.PollTrigger(ctx, "nightly"); err == nil {
		t.Error("PollTrigger on a schedule succeeded")
	} else {
		e := apiStatus(t, err, http.StatusBadRequest)
		if !strings.Contains(e.Message, "no poll") {
			t.Errorf("PollTrigger error = %q, want it to say the source has no poll", e.Message)
		}
	}
	j, err := h.c.TestTrigger(ctx, "nightly", map[string]any{
		"id": "2026-09-21T07:00:00.000000000Z", "scheduled_at": "2026-09-21T07:00:00.000000000Z",
	})
	if err != nil {
		t.Fatalf("TestTrigger: %v", err)
	}
	if j.Action == nil || !strings.Contains(string(j.Action.Body), "2026-09-21") {
		t.Errorf("judgement = %+v, want the title rendered over the occurrence", j)
	}

	// A cron definition is proven through the validator rather than by
	// waiting: cron's finest granularity is a minute.
	v, err := h.c.ValidateTrigger(ctx, fmt.Sprintf(`id: weekdays
source:
  type: schedule
  project: %d
  cron: "0 9 * * 1-5"
  timezone: UTC
action:
  type: create_task
  title: 'sweep {{ .Event.date }}'
`, h.projectID), "weekdays")
	if err != nil || !v.Valid {
		t.Fatalf("ValidateTrigger on a cron schedule = %+v, %v", v, err)
	}

	// The descriptor a form renders carries the variant and its three fields.
	schema, err := h.c.TriggerSchema(ctx)
	if err != nil {
		t.Fatalf("TriggerSchema: %v", err)
	}
	var fields []string
	for _, s := range schema.Sources {
		if s.Type == "schedule" {
			for _, f := range s.Fields {
				fields = append(fields, f.Name)
			}
		}
	}
	if want := []string{"type", "project", "cron", "every", "timezone"}; !slices.Equal(fields, want) {
		t.Errorf("schedule variant fields = %v, want %v", fields, want)
	}
}
