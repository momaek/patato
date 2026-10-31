// Package jobs 用 River（基于 PostgreSQL 的任务队列）运行周期任务和通知推送。
// 周期任务只在 River 选出的领导实例上调度，多实例部署不会重复执行。
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// Migrate 创建或升级 River 自己的表。
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	m, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return err
	}
	_, err = m.Migrate(ctx, rivermigrate.DirectionUp, nil)
	return err
}

// Task 是一个周期任务。
type Task struct {
	Name  string
	Every time.Duration
	Run   func(ctx context.Context) error
}

type TickArgs struct {
	Task string `json:"task"`
}

func (TickArgs) Kind() string { return "tick" }

type tickWorker struct {
	river.WorkerDefaults[TickArgs]
	tasks map[string]Task
}

func (w *tickWorker) Work(ctx context.Context, job *river.Job[TickArgs]) error {
	t, ok := w.tasks[job.Args.Task]
	if !ok {
		return river.JobCancel(fmt.Errorf("unknown task %q", job.Args.Task))
	}
	return t.Run(ctx)
}

// Timeout 让周期任务不会无限运行，超时后由下一轮接着处理。
func (w *tickWorker) Timeout(*river.Job[TickArgs]) time.Duration { return 5 * time.Minute }

// NotifyArgs 是一次推送：事件告警，或审批卡片的创建与更新。
type NotifyArgs struct {
	EventID    int64  `json:"event_id,omitempty"`
	ApprovalID string `json:"approval_id,omitempty"`
	Action     string `json:"action,omitempty"`
}

func (NotifyArgs) Kind() string { return "notify" }

// Sender 执行实际的推送。
type Sender interface {
	SendEvent(ctx context.Context, id int64) error
	SendApproval(ctx context.Context, id string) error
	UpdateApproval(ctx context.Context, id string) error
}

type notifyWorker struct {
	river.WorkerDefaults[NotifyArgs]
	sender Sender
}

func (w *notifyWorker) Work(ctx context.Context, job *river.Job[NotifyArgs]) error {
	a := job.Args
	switch {
	case a.EventID != 0:
		return w.sender.SendEvent(ctx, a.EventID)
	case a.Action == "created":
		return w.sender.SendApproval(ctx, a.ApprovalID)
	case a.Action == "decided":
		return w.sender.UpdateApproval(ctx, a.ApprovalID)
	}
	return nil
}

type Runner struct {
	client *river.Client[pgx.Tx]
	log    *slog.Logger
}

// New 创建任务调度器。sender 为 nil 时不推送通知（未配置 Telegram）。
func New(pool *pgxpool.Pool, tasks []Task, sender Sender, log *slog.Logger) (*Runner, error) {
	workers := river.NewWorkers()
	byName := map[string]Task{}
	var periodic []*river.PeriodicJob
	for _, t := range tasks {
		t := t
		byName[t.Name] = t
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(t.Every),
			func() (river.JobArgs, *river.InsertOpts) {
				return TickArgs{Task: t.Name}, &river.InsertOpts{MaxAttempts: 1}
			},
			&river.PeriodicJobOpts{RunOnStart: true},
		))
	}
	river.AddWorker(workers, &tickWorker{tasks: byName})
	if sender == nil {
		sender = noopSender{}
	}
	river.AddWorker(workers, &notifyWorker{sender: sender})
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger:       log,
		Queues:       map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 10}},
		Workers:      workers,
		PeriodicJobs: periodic,
	})
	if err != nil {
		return nil, err
	}
	return &Runner{client: client, log: log}, nil
}

func (r *Runner) Start(ctx context.Context) error { return r.client.Start(ctx) }

func (r *Runner) Stop(ctx context.Context) error { return r.client.Stop(ctx) }

func (r *Runner) enqueue(ctx context.Context, args NotifyArgs) {
	if _, err := r.client.Insert(ctx, args, &river.InsertOpts{MaxAttempts: 5}); err != nil {
		r.log.Error("enqueue notification", "err", err)
	}
}

// EventCreated 实现 events.Notifier。
func (r *Runner) EventCreated(ctx context.Context, id int64) { r.enqueue(ctx, NotifyArgs{EventID: id}) }

// ApprovalCreated 实现 change.ApprovalNotifier。
func (r *Runner) ApprovalCreated(ctx context.Context, id string) {
	r.enqueue(ctx, NotifyArgs{ApprovalID: id, Action: "created"})
}

// ApprovalDecided 实现 approval.Notifier。
func (r *Runner) ApprovalDecided(ctx context.Context, id string) {
	r.enqueue(ctx, NotifyArgs{ApprovalID: id, Action: "decided"})
}

type noopSender struct{}

func (noopSender) SendEvent(context.Context, int64) error       { return nil }
func (noopSender) SendApproval(context.Context, string) error   { return nil }
func (noopSender) UpdateApproval(context.Context, string) error { return nil }
