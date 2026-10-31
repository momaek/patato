package billing

import (
	"testing"
	"time"
)

func TestNextResetPurchaseDayClampsToMonthEnd(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	anchor := time.Date(2026, 1, 31, 10, 0, 0, 0, loc)
	from := time.Date(2026, 2, 1, 0, 0, 0, 0, loc)
	got := NextReset(ResetPurchaseDay, &anchor, from, loc)
	want := time.Date(2026, 2, 28, 10, 0, 0, 0, loc)
	if got == nil || !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// 3 月有 31 日，回到锚点那一天
	got = NextReset(ResetPurchaseDay, &anchor, want, loc)
	want = time.Date(2026, 3, 31, 10, 0, 0, 0, loc)
	if got == nil || !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestNextResetCalendarMonth(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	from := time.Date(2026, 12, 15, 23, 0, 0, 0, loc)
	got := NextReset(ResetCalendarMonth, nil, from, loc)
	want := time.Date(2027, 1, 1, 0, 0, 0, 0, loc)
	if got == nil || !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestNextResetNever(t *testing.T) {
	if NextReset(ResetNever, nil, time.Now(), time.UTC) != nil {
		t.Fatal("never should not schedule a reset")
	}
}

func TestGB(t *testing.T) {
	if GB(1) != 1<<30 {
		t.Fatal("1 GB must equal 1024^3 bytes, same as Xboard")
	}
	if ToGB(GB(300)) != 300 {
		t.Fatal("round trip")
	}
}
