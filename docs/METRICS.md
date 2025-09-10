# Metrics and Dashboards

This document describes the Prometheus metrics exposed by the MultiNIC Agent and recommended dashboard setup.

## Prometheus Endpoint
- Controller exposes `/metrics` on `CONTROLLER_METRICS_PORT` (default: `9090`).
- Scrape example (Prometheus Operator ServiceMonitor): ensure the controller Service targets the metrics port.

## Worker Pool Metrics
- `multinic_worker_queue_depth{pool}` gauge: queued jobs.
- `multinic_worker_active{pool}` gauge: active workers.
- `multinic_worker_utilization{pool}` gauge: active/total.
- `multinic_worker_task_duration_seconds{pool,status}` histogram: task latency.
  - Recommended buckets (seconds): `0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60`.
  - `status` in `success|failed|panic|retried`.
- `multinic_worker_task_retries_total{pool}` counter.
- `multinic_worker_panics_total{pool}` counter.

## Interface/DB/Polling Metrics
- `multinic_interfaces_processed_total{status}` counter.
- `multinic_interface_processing_duration_seconds{interface_name,status}` histogram.
- `multinic_polling_cycles_total` counter, `multinic_polling_cycle_duration_seconds` histogram.
- `multinic_db_connection_status` gauge, `multinic_db_query_duration_seconds{query_type}` histogram.
- `multinic_configuration_drifts_total{drift_type}` counter.

## SLO Hints
- Availability: ratio of `success / (success+failed)` over 5m.
- Latency: `p50/p90/p99` of `multinic_worker_task_duration_seconds` by `pool`.
- Backpressure: queue depth near steady-state; alert if `> 10 * workers` for 5m.
- Reliability: spikes in `retries_total`/`panics_total`.

## Grafana Panels (recommended)
- Queue depth (instant): `multinic_worker_queue_depth{pool="configure"}`.
- Active workers: `multinic_worker_active{pool="configure"}`.
- Utilization: `multinic_worker_utilization{pool="configure"}`.
- Task duration p50/p90/p99: histogram_quantile over `multinic_worker_task_duration_seconds_bucket`.
- Retry/Panic rates: per-5m increase of counters.

See `docs/grafana/workerpool-dashboard.json` for a starter dashboard.

