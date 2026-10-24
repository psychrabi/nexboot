package tasks

import (
	"context"
	"time"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

func initialTaskStatus(async bool) domain.TaskStatus {
	if async {
		return domain.TaskStatusPending
	}
	return domain.TaskStatusRunning
}

func launchTask(st store.Store, task domain.Task, run func(context.Context, domain.Task) error, fail func(context.Context, domain.Task, error) error) {
	go func() {
		ctx := context.Background()
		task.Status = domain.TaskStatusRunning
		task.Progress = 0
		if err := st.Tasks().Update(ctx, task); err != nil {
			_ = fail(ctx, task, err)
			return
		}
		if err := run(ctx, task); err != nil {
			_ = fail(ctx, task, err)
		}
	}()
}

func FailInterruptedTasks(ctx context.Context, st store.Store, now func() time.Time) error {
	tasks, err := st.Tasks().List(ctx)
	if err != nil {
		return err
	}
	sessions, err := st.PESessions().ListActive(ctx)
	if err != nil {
		return err
	}
	recoverable := make(map[string]domain.PESessionState, len(sessions))
	installations, err := st.InstallSessions().List(ctx)
	if err != nil {
		return err
	}
	for _, v := range installations {
		if v.Active() {
			recoverable[v.TaskID] = domain.PEStatePending
		}
	}
	for _, session := range sessions {
		recoverable[session.TaskID] = session.State
	}
	finished := now()
	for _, task := range tasks {
		if state, ok := recoverable[task.ID]; ok {
			if task.Status == domain.TaskStatusFailed && task.Error == "service restarted during task execution" {
				task.Status = domain.TaskStatusRunning
				if state == domain.PEStatePending || state == domain.PEStatePreparing {
					task.Status = domain.TaskStatusPending
				}
				task.Error, task.FinishedAt = "", nil
				if err := st.Tasks().Update(ctx, task); err != nil {
					return err
				}
			}
			continue
		}
		if task.Status != domain.TaskStatusPending && task.Status != domain.TaskStatusRunning {
			continue
		}
		task.Status = domain.TaskStatusFailed
		task.Error = "service restarted during task execution"
		task.FinishedAt = &finished
		if err := st.Tasks().Update(ctx, task); err != nil {
			return err
		}
	}
	return nil
}
