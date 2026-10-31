package billing

import "time"

// 流量重置方式，取值与 plans.reset_method 一致。
const (
	ResetPurchaseDay   = "purchase_day"
	ResetCalendarMonth = "calendar_month"
	ResetNever         = "never"
)

// NextReset 计算 from 之后的下一次流量重置时间；不重置时返回 nil。
//
// purchase_day：以 anchor（开通时间）为准，每月同一天重置；当月没有这一天时取月末。
// calendar_month：每月 1 日 0 点（按 loc 时区）。
func NextReset(method string, anchor *time.Time, from time.Time, loc *time.Location) *time.Time {
	switch method {
	case ResetCalendarMonth:
		f := from.In(loc)
		t := time.Date(f.Year(), f.Month()+1, 1, 0, 0, 0, 0, loc)
		return &t
	case ResetPurchaseDay:
		a := from
		if anchor != nil {
			a = *anchor
		}
		a = a.In(loc)
		for k := 1; k <= 1200; k++ {
			t := addMonthsClamped(a, k)
			if t.After(from) {
				return &t
			}
		}
		return nil
	default:
		return nil
	}
}

// addMonthsClamped 加 n 个月，日期超出当月天数时取当月最后一天。
func addMonthsClamped(t time.Time, n int) time.Time {
	y, m, d := t.Date()
	first := time.Date(y, m+time.Month(n), 1, t.Hour(), t.Minute(), t.Second(), 0, t.Location())
	last := first.AddDate(0, 1, -1).Day()
	if d > last {
		d = last
	}
	return time.Date(first.Year(), first.Month(), d, t.Hour(), t.Minute(), t.Second(), 0, t.Location())
}

// GB 把以 GB 计的流量换成字节，与 Xboard 的换算一致（1 GB = 1024³ 字节）。
func GB(gb float64) int64 { return int64(gb * 1024 * 1024 * 1024) }

// ToGB 把字节换成 GB，用于展示。
func ToGB(b int64) float64 { return float64(b) / (1024 * 1024 * 1024) }
