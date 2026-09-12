package store

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestPendingCloneJobsKeyset(t *testing.T) {
	db, _, _ := nativeAccountFixture(t, "linux")
	for i := range 5 {
		id := fmt.Sprintf("pending-%d", i)
		job, _, err := db.CreateJob(t.Context(), Job{ID: id, IdempotencyKey: id, Operation: "pve.template_clone", State: "accepted", SourceVMID: 9202, TargetVMID: 9300 + i, TargetNode: "test", TaskNode: "test", Request: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		if i != 2 {
			if err := db.RecordCloneTaskHandle(t.Context(), job, "task-"+id); err != nil {
				t.Fatal(err)
			}
		}
	}
	cursor := ""
	var all []string
	for {
		ids, err := db.PendingCloneJobIDs(t.Context(), cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) == 0 {
			break
		}
		all = append(all, ids...)
		cursor = ids[len(ids)-1]
	}
	if fmt.Sprint(all) != "[pending-0 pending-1 pending-3 pending-4]" {
		t.Fatalf("keyset lost tasks: %v", all)
	}
	if _, err := db.CompleteJobTask(t.Context(), "pending-0", "succeeded", "task-pending-0", ""); err != nil {
		t.Fatal(err)
	}
	ids, err := db.PendingCloneJobIDs(t.Context(), "", 100)
	if err != nil || fmt.Sprint(ids) != "[pending-1 pending-3 pending-4]" {
		t.Fatalf("terminal tasks not excluded: %v %v", ids, err)
	}
}
