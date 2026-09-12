package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestCloneTaskHandleIsBoundAndCannotReopenTerminalJob(t *testing.T) {
	db, _, _ := nativeAccountFixture(t, "linux")
	job, _, err := db.ReserveDesktopCloneJob(t.Context(), Job{ID: "handle-test", IdempotencyKey: "handle-test", Operation: "pve.template_clone", State: "accepted", SourceVMID: 9000, TargetVMID: 9203, TargetNode: "target", TaskNode: "source", Request: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Job){func(j *Job) { j.TargetVMID++ }, func(j *Job) { j.SourceVMID++ }, func(j *Job) { j.TargetNode = "other" }, func(j *Job) { j.TaskNode = "other" }, func(j *Job) { j.Operation = "image.build" }} {
		wrong := job
		mutate(&wrong)
		if err := db.RecordCloneTaskHandle(t.Context(), wrong, "task-a"); !errors.Is(err, ErrConflict) {
			t.Fatalf("mismatched target accepted: %v", err)
		}
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, handle := range []string{"task-a", "task-b"} {
		go func() { <-start; results <- db.RecordCloneTaskHandle(t.Context(), job, handle) }()
	}
	close(start)
	success := 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("concurrent handles accepted=%d", success)
	}
	saved, err := db.JobByID(t.Context(), job.ID)
	if err != nil || saved.State != "running" || saved.UPID == "" {
		t.Fatalf("missing handle: %+v %v", saved, err)
	}
	if err := db.RecordCloneTaskHandle(t.Context(), job, saved.UPID); err != nil {
		t.Fatalf("same handle replay: %v", err)
	}
	if completed, err := db.CompleteJobTask(t.Context(), job.ID, "succeeded", saved.UPID, ""); err != nil || !completed {
		t.Fatalf("completion: %v %v", completed, err)
	}
	if err := db.RecordCloneTaskHandle(t.Context(), job, saved.UPID); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal job reopened: %v", err)
	}
	final, err := db.JobByID(t.Context(), job.ID)
	if err != nil || final.State != "succeeded" || final.UPID != saved.UPID {
		t.Fatalf("terminal job altered: %+v %v", final, err)
	}
}

func TestCloneTargetReservationHasOneOwnerAndSurvivesFailure(t *testing.T) {
	db, _, _ := nativeAccountFixture(t, "linux")
	const requests = 8
	start := make(chan struct{})
	type outcome struct {
		job     Job
		created bool
		err     error
	}
	results := make(chan outcome, requests)
	for i := range requests {
		go func() {
			<-start
			job := Job{ID: fmt.Sprintf("clone-owner-%d", i), IdempotencyKey: fmt.Sprintf("clone-key-%d", i), RequestFingerprint: fmt.Sprintf("fingerprint-%d", i), Operation: "pve.template_clone", State: "accepted", SourceVMID: 9000, TargetVMID: 9203, Request: json.RawMessage(`{}`)}
			saved, created, err := db.ReserveDesktopCloneJob(t.Context(), job)
			saved.RequestFingerprint = job.RequestFingerprint
			results <- outcome{saved, created, err}
		}()
	}
	close(start)
	var winner Job
	successes := 0
	for range requests {
		r := <-results
		if r.err == nil {
			if !r.created {
				t.Error("distinct request reused another owner")
			}
			successes++
			winner = r.job
		} else if !errors.Is(r.err, ErrConflict) {
			t.Errorf("unexpected error: %v", r.err)
		}
	}
	if successes != 1 {
		t.Fatalf("target owners=%d", successes)
	}
	if err := db.UpdateJobTask(t.Context(), winner.ID, "failed", "", "uncertain upstream result"); err != nil {
		t.Fatal(err)
	}
	replay := winner
	replay.State = "accepted"
	prior, created, err := db.ReserveDesktopCloneJob(t.Context(), replay)
	if err != nil || created || prior.ID != winner.ID || prior.State != "failed" {
		t.Fatalf("failed replay lost ownership: %+v %v %v", prior, created, err)
	}
	rival := replay
	rival.ID = "rival"
	rival.IdempotencyKey = "rival-key"
	if _, _, err := db.ReserveDesktopCloneJob(t.Context(), rival); !errors.Is(err, ErrConflict) {
		t.Fatalf("failed target reused: %v", err)
	}
	replay.RequestFingerprint = "changed"
	if _, _, err := db.ReserveDesktopCloneJob(t.Context(), replay); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay accepted: %v", err)
	}
	rival.TargetVMID = 9001
	if _, _, err := db.ReserveDesktopCloneJob(t.Context(), rival); !errors.Is(err, ErrConflict) {
		t.Fatalf("existing desktop overwritten: %v", err)
	}
	var count int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM pve_jobs WHERE operation='pve.template_clone'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("reservation leaked jobs: %d %v", count, err)
	}
}

func TestAvailableCloneTargetsSkipInventoryAndReservations(t *testing.T) {
	db, _, _ := nativeAccountFixture(t, "linux")
	base := Job{ID: "old", IdempotencyKey: "old", Operation: "pve.template_clone", State: "failed", SourceVMID: 9000, TargetVMID: 9204, Request: json.RawMessage(`{}`)}
	if _, _, err := db.CreateJob(t.Context(), base); err != nil {
		t.Fatal(err)
	}
	type result struct {
		job Job
		err error
	}
	results := make(chan result, 4)
	start := make(chan struct{})
	for i := range 4 {
		go func() {
			<-start
			job := base
			job.ID = fmt.Sprintf("available-%d", i)
			job.IdempotencyKey = job.ID
			job.State = "accepted"
			job.TargetVMID = 9203
			saved, created, err := db.ReserveAvailableDesktopCloneJob(t.Context(), job, []int{9203, 9205})
			if err == nil && !created {
				err = fmt.Errorf("new request reused a job")
			}
			results <- result{saved, err}
		}()
	}
	close(start)
	seen := map[int]bool{}
	for range 4 {
		r := <-results
		if r.err != nil || r.job.TargetVMID < 9206 || r.job.TargetVMID > 9209 || seen[r.job.TargetVMID] {
			t.Fatalf("invalid candidate: %+v %v", r.job, r.err)
		}
		seen[r.job.TargetVMID] = true
		request := r.job
		request.TargetVMID = 9203
		replay, created, err := db.ReserveAvailableDesktopCloneJob(t.Context(), request, nil)
		if err != nil || created || replay.TargetVMID != r.job.TargetVMID {
			t.Fatalf("replay changed candidate: %+v %v %v", replay, created, err)
		}
	}
	blocked := make([]int, 1024)
	for i := range blocked {
		blocked[i] = 10000 + i
	}
	base.ID = "exhausted"
	base.IdempotencyKey = "exhausted"
	base.State = "accepted"
	base.TargetVMID = 10000
	if _, _, err := db.ReserveAvailableDesktopCloneJob(t.Context(), base, blocked); !errors.Is(err, ErrConflict) {
		t.Fatalf("scan budget ignored: %v", err)
	}
	if _, err := db.JobByID(t.Context(), base.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("exhaustion left a job: %v", err)
	}
	if saved, created, err := db.ReserveAvailableDesktopCloneJob(t.Context(), base, nil); err != nil || !created || saved.TargetVMID != 10000 {
		t.Fatalf("empty inventory could not reserve a candidate: %+v %v %v", saved, created, err)
	}
}
