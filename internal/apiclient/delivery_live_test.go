package apiclient_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/store"
)

// The wire round trip, against the real handlers (task 137.2): a running
// task records its delivery, which comes back from GetTaskDelivery and on
// GetTask in full, and on the list row as a summary. A task that is not
// running or done is a 409.
func TestDeliveryOverTheWire(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	h.setState(t, h.taskID, store.TaskRunning)

	stored, err := c.SetTaskDelivery(t.Context(), h.taskID, apiclient.DeliveryInput{
		Kind: apiclient.DeliveryReply, Title: "Answered", Body: "It is in §7.", CloseReason: "not_planned",
	})
	if err != nil {
		t.Fatalf("SetTaskDelivery: %v", err)
	}
	if stored.Kind != apiclient.DeliveryReply || stored.Title != "Answered" || stored.Body != "It is in §7." ||
		stored.CloseReason != "not_planned" || stored.Implicit || stored.RecordedAt.IsZero() {
		t.Errorf("stored = %+v", stored)
	}
	got, err := c.GetTaskDelivery(t.Context(), h.taskID)
	if err != nil || got == nil || got.Body != "It is in §7." {
		t.Fatalf("GetTaskDelivery = %+v, %v", got, err)
	}
	detail, err := c.GetTask(t.Context(), h.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Delivery == nil || detail.Delivery.Body != "It is in §7." {
		t.Errorf("GetTask delivery = %+v, want it in full", detail.Delivery)
	}
	tasks, err := c.ListTasks(t.Context(), apiclient.ListTasksOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.ID == h.taskID && (task.Delivery == nil || task.Delivery.Title != "Answered" || task.Delivery.Body != "") {
			t.Errorf("list row delivery = %+v, want the summary", task.Delivery)
		}
	}

	h.setState(t, h.taskID, store.TaskPaused)
	_, err = c.SetTaskDelivery(t.Context(), h.taskID, apiclient.DeliveryInput{Title: "late"})
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Fatalf("SetTaskDelivery on a paused task = %v, want a 409", err)
	}
}

// The client's copy of the event name has to be the daemon's.
func TestDeliveryEventNameMatchesTheDaemon(t *testing.T) {
	if apiclient.EventTaskDeliveryChanged != store.EventTaskDeliveryChanged {
		t.Errorf("client event name %q != daemon's %q",
			apiclient.EventTaskDeliveryChanged, store.EventTaskDeliveryChanged)
	}
	for _, k := range []struct{ client, daemon string }{
		{apiclient.DeliveryChange, string(store.DeliveryChange)},
		{apiclient.DeliveryReply, string(store.DeliveryReply)},
		{apiclient.DeliveryNone, string(store.DeliveryNone)},
	} {
		if k.client != k.daemon {
			t.Errorf("client kind %q != daemon's %q", k.client, k.daemon)
		}
	}
}
