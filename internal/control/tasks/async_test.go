package tasks

import (
	"context"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

func TestFailInterruptedTasks(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 4, 14, 45, 0, 0, time.UTC)
	tasks := []domain.Task{
		{ID: "t-pending", Type: domain.TaskTypeImportImage, Status: domain.TaskStatusPending, CreatedAt: now},
		{ID: "t-running", Type: domain.TaskTypeImportImage, Status: domain.TaskStatusRunning, CreatedAt: now},
		{ID: "t-success", Type: domain.TaskTypeImportImage, Status: domain.TaskStatusSuccess, CreatedAt: now},
	}
	for _, task := range tasks {
		if err := st.Tasks().Create(ctx, task); err != nil {
			t.Fatalf("create task %s: %v", task.ID, err)
		}
	}

	if err := FailInterruptedTasks(ctx, st, func() time.Time { return now.Add(time.Minute) }); err != nil {
		t.Fatal(err)
	}

	check := func(id string, want domain.TaskStatus) domain.Task {
		task, err := st.Tasks().Get(ctx, id)
		if err != nil {
			t.Fatalf("get task %s: %v", id, err)
		}
		if task.Status != want {
			t.Fatalf("%s status = %s, want %s", id, task.Status, want)
		}
		return task
	}

	pending := check("t-pending", domain.TaskStatusFailed)
	running := check("t-running", domain.TaskStatusFailed)
	_ = check("t-success", domain.TaskStatusSuccess)

	for _, task := range []domain.Task{pending, running} {
		if task.Error != "service restarted during task execution" {
			t.Fatalf("%s error = %q", task.ID, task.Error)
		}
		if task.FinishedAt == nil || !task.FinishedAt.Equal(now.Add(time.Minute)) {
			t.Fatalf("%s finished_at = %#v", task.ID, task.FinishedAt)
		}
	}
}

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), "file:"+t.TempDir()+"/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestFailInterruptedTasksPreservesRecoverablePESessions(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, state := range []domain.PESessionState{domain.PEStatePending, domain.PEStateBooted, domain.PEStateFinalizing, domain.PEStateDone} {
		id := string(state)
		if err := st.Tasks().Create(ctx, domain.Task{ID: id, Type: domain.TaskTypeModelAdapt, Status: domain.TaskStatusRunning, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := st.PESessions().Create(ctx, domain.PESession{ID: id, TerminalID: id, TaskID: id, Token: id, State: state, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := FailInterruptedTasks(ctx, st, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"pending", "booted", "finalizing", "done"} {
		task, err := st.Tasks().Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		want := domain.TaskStatusRunning
		if id == "done" {
			want = domain.TaskStatusFailed
		}
		if task.Status != want {
			t.Fatalf("%s status=%s, want %s", id, task.Status, want)
		}
	}
}

func TestFailInterruptedTasksRecoversPEFailedByPreviousVersion(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	task := domain.Task{ID: "capture", Type: domain.TaskTypeIdentityCapture, Status: domain.TaskStatusFailed, Error: "service restarted during task execution", FinishedAt: &now, CreatedAt: now}
	if err := st.Tasks().Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := st.PESessions().Create(ctx, domain.PESession{ID: "capture", TerminalID: "t", TaskID: task.ID, Token: "capture", State: domain.PEStatePending, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := FailInterruptedTasks(ctx, st, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	task, err := st.Tasks().Get(ctx, task.ID)
	if err != nil || task.Status != domain.TaskStatusPending || task.Error != "" || task.FinishedAt != nil {
		t.Fatalf("recoverable task=%+v err=%v", task, err)
	}
}
