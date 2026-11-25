package telemetry

import "github.com/prometheus/client_golang/prometheus"

var (
	signalLabel = []string{"signal"}

	ingestAcceptedDesc = prometheus.NewDesc("cadence_otlp_ingest_accepted_total",
		"OTLP log records or metric points the devices sent that were stored.", signalLabel, nil)
	ingestRejectedDesc = prometheus.NewDesc("cadence_otlp_ingest_rejected_total",
		"OTLP log records or metric points refused in a partial success.", signalLabel, nil)
	ingestRateLimitedDesc = prometheus.NewDesc("cadence_otlp_ingest_rate_limited_total",
		"OTLP requests answered 429: a device over its rate, or with one of the signal under way.", signalLabel, nil)
	ingestBusyDesc = prometheus.NewDesc("cadence_otlp_ingest_busy_total",
		"OTLP requests answered 503 for want of a decompression slot.", signalLabel, nil)
	ingestFailedDesc = prometheus.NewDesc("cadence_otlp_ingest_failed_total",
		"OTLP requests refused for another reason, by code.", []string{"signal", "code"}, nil)
	exportSentDesc = prometheus.NewDesc("cadence_otlp_export_sent_total",
		"Batches the OTLP export delivered.", signalLabel, nil)
	exportDroppedDesc = prometheus.NewDesc("cadence_otlp_export_dropped_total",
		"Batches the OTLP export dropped: past its queue, refused by the collector, or waiting when the export was turned off.",
		signalLabel, nil)
	exportFailuresDesc = prometheus.NewDesc("cadence_otlp_export_failures_total",
		"OTLP export attempts that failed.", nil, nil)
	exportQueuedDesc = prometheus.NewDesc("cadence_otlp_export_queued",
		"Batches waiting for the OTLP export.", nil, nil)
	serverLogDroppedDesc = prometheus.NewDesc("cadence_server_log_dropped_total",
		"The server's own log lines dropped before they were stored.", nil, nil)
)

// serverCollector reads the server's own counts at each scrape.
type serverCollector struct{ d PromDeps }

func (serverCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{
		ingestAcceptedDesc, ingestRejectedDesc, ingestRateLimitedDesc, ingestBusyDesc, ingestFailedDesc,
		exportSentDesc, exportDroppedDesc, exportFailuresDesc, exportQueuedDesc, serverLogDroppedDesc,
	} {
		ch <- d
	}
}

func (c serverCollector) Collect(ch chan<- prometheus.Metric) {
	counter := func(d *prometheus.Desc, v uint64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.CounterValue, float64(v), labels...)
	}
	var logs, metrics SignalStats
	if c.d.Ingest != nil {
		logs, metrics = c.d.Ingest.Stats()
	}
	for signal, s := range map[string]SignalStats{kindLogs: logs, kindMetrics: metrics} {
		counter(ingestAcceptedDesc, s.Accepted, signal)
		counter(ingestRejectedDesc, s.Rejected, signal)
		counter(ingestRateLimitedDesc, s.RateLimited, signal)
		counter(ingestBusyDesc, s.Busy, signal)
		for _, code := range failCodes {
			counter(ingestFailedDesc, s.Failed[code], signal, code)
		}
	}
	var ex ExportStats
	if c.d.Export != nil {
		ex = c.d.Export.Stats()
	}
	for signal, s := range map[string]ExportSignal{kindLogs: ex.Logs, kindMetrics: ex.Metrics} {
		counter(exportSentDesc, s.Sent, signal)
		counter(exportDroppedDesc, s.Dropped, signal)
	}
	counter(exportFailuresDesc, ex.Failures)
	ch <- prometheus.MustNewConstMetric(exportQueuedDesc, prometheus.GaugeValue, float64(ex.Queued))
	var dropped uint64
	if c.d.SelfDropped != nil {
		dropped = c.d.SelfDropped()
	}
	counter(serverLogDroppedDesc, dropped)
}
