// Package events 记录系统事件（节点掉线、用户到期、流量超额等），并在新事件产生后通知推送。
package events

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

const (
	NodeOffline   = "node_offline"
	NodeOnline    = "node_online"
	UserExpired   = "user_expired"
	UserOverQuota = "user_over_quota"
	PanelHostExec = "panel_host_exec"
	ApprovalNew   = "approval_created"
)

// Notifier 在事件写入后被调用，负责推送到 Telegram 等渠道。
type Notifier interface {
	EventCreated(ctx context.Context, id int64)
}

type Recorder struct {
	st       *store.Store
	notifier Notifier
	log      *slog.Logger
}

func NewRecorder(st *store.Store, log *slog.Logger) *Recorder { return &Recorder{st: st, log: log} }

func (r *Recorder) SetNotifier(n Notifier) { r.notifier = n }

type Event struct {
	Kind     string
	Severity string
	Subject  string
	Message  string
	Payload  any
	// DedupKey 非空时，同一个键在解决之前只记录一次。
	DedupKey string
}

// Emit 写入事件；同一个去重键已有未解决的事件时什么也不做。
func (r *Recorder) Emit(ctx context.Context, q *db.Queries, e Event) {
	if q == nil {
		q = r.st.Queries
	}
	payload, _ := json.Marshal(e.Payload)
	if e.Payload == nil {
		payload = []byte("{}")
	}
	var dedup *string
	if e.DedupKey != "" {
		dedup = &e.DedupKey
	}
	if e.Severity == "" {
		e.Severity = "info"
	}
	row, err := q.InsertEvent(ctx, db.InsertEventParams{
		Kind: e.Kind, Severity: e.Severity, Subject: e.Subject, Message: e.Message, Payload: payload, DedupKey: dedup,
	})
	if store.IsNotFound(err) {
		return
	}
	if err != nil {
		r.log.Error("record event", "kind", e.Kind, "err", err)
		return
	}
	if r.notifier != nil {
		r.notifier.EventCreated(context.WithoutCancel(ctx), row.ID)
	}
}

// Resolve 把某个去重键对应的未解决事件标记为已解决。
func (r *Recorder) Resolve(ctx context.Context, q *db.Queries, dedupKey string) {
	if q == nil {
		q = r.st.Queries
	}
	if err := q.ResolveEvents(ctx, &dedupKey); err != nil {
		r.log.Error("resolve event", "key", dedupKey, "err", err)
	}
}
