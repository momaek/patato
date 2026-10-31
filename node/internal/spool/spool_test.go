package spool

import (
	"io"
	"log/slog"
	"testing"

	"github.com/momaek/patato/nodeapi"
)

func TestSpool(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := Open(dir, log)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Append(nodeapi.ReportRequest{Nodes: []nodeapi.NodeReport{{ID: int64(i)}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Ack(2); err != nil {
		t.Fatal(err)
	}
	// 重新打开：队列 ID 不变，序号接着往下走，未确认的批次还在
	s2, err := Open(dir, log)
	if err != nil {
		t.Fatal(err)
	}
	if s2.ID() != s.ID() {
		t.Fatal("spool id changed after reopen")
	}
	p, err := s2.Pending()
	if err != nil || len(p) != 1 || p[0].Seq != 3 || p[0].Spool != s.ID() || p[0].Nodes[0].ID != 2 {
		t.Fatalf("pending: %+v %v", p, err)
	}
	seq, _ := s2.Append(nodeapi.ReportRequest{})
	if seq != 4 {
		t.Fatalf("next seq = %d", seq)
	}
}
