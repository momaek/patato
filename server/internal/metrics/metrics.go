// Package metrics 定义 Patato 的 Prometheus 指标。
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// NodeRequests 统计节点接口的请求，按接口和结果区分。
	NodeRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "patato_node_requests_total",
		Help: "UniProxy requests from node backends.",
	}, []string{"endpoint", "result"})

	// TrafficReports 统计收到并落库的流量上报。
	TrafficReports = promauto.NewCounter(prometheus.CounterOpts{
		Name: "patato_traffic_reports_received_total",
		Help: "Traffic reports stored for aggregation.",
	})

	// TrafficProcessed 统计已聚合入账的流量上报。
	TrafficProcessed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "patato_traffic_reports_processed_total",
		Help: "Traffic reports aggregated into user usage.",
	})

	// Changes 统计变更计划的执行结果。
	Changes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "patato_changes_total",
		Help: "Change plans by kind and outcome.",
	}, []string{"kind", "status"})
)
