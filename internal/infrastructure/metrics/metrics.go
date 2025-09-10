package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// 인터페이스 처리 관련 메트릭
	InterfacesProcessed = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "multinic_interfaces_processed_total",
			Help: "Total number of network interfaces processed",
		},
		[]string{"status"}, // success, failed
	)

	InterfaceProcessingDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "multinic_interface_processing_duration_seconds",
			Help:    "Time spent processing each network interface",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"interface_name", "status"},
	)

	// 폴링 관련 메트릭
	PollingCycleCount = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "multinic_polling_cycles_total",
			Help: "Total number of polling cycles executed",
		},
	)

	PollingCycleDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "multinic_polling_cycle_duration_seconds",
			Help:    "Time spent in each polling cycle",
			Buckets: prometheus.DefBuckets,
		},
	)

	PollingBackoffLevel = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "multinic_polling_backoff_level",
			Help: "Current backoff level (0 = no backoff)",
		},
	)

	// 데이터베이스 연결 관련 메트릭
	DBConnectionStatus = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "multinic_db_connection_status",
			Help: "Database connection status (1 = connected, 0 = disconnected)",
		},
	)

	DBQueryDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "multinic_db_query_duration_seconds",
			Help:    "Time spent executing database queries",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"query_type"}, // get_pending, update_status, etc.
	)

	// 동시 처리 관련 메트릭
	ConcurrentTasks = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "multinic_concurrent_tasks",
			Help: "Number of interfaces being processed concurrently",
		},
	)

	// 워커풀 메트릭 (Phase 2)
	WorkerQueueDepth = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "multinic_worker_queue_depth",
			Help: "Current queued jobs in worker pool",
		},
		[]string{"pool"},
	)

	WorkerActive = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "multinic_worker_active",
			Help: "Number of active workers processing jobs",
		},
		[]string{"pool"},
	)

	WorkerUtilization = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "multinic_worker_utilization",
			Help: "Worker utilization ratio (active/total)",
		},
		[]string{"pool"},
	)

	WorkerTaskDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "multinic_worker_task_duration_seconds",
			Help:    "Task processing duration per worker pool",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"pool", "status"}, // status: success, failed, panic, retried
	)

	WorkerRetries = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "multinic_worker_task_retries_total",
			Help: "Total number of task retries",
		},
		[]string{"pool"},
	)

	WorkerPanics = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "multinic_worker_panics_total",
			Help: "Total number of panics recovered in workers",
		},
		[]string{"pool"},
	)

	// 드리프트 감지 메트릭
	ConfigurationDrifts = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "multinic_configuration_drifts_total",
			Help: "Total number of configuration drifts detected",
		},
		[]string{"drift_type"}, // ip_address, mtu, missing_file, etc.
	)

	// 고아 인터페이스 정리 메트릭
	OrphanedInterfacesDeleted = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "multinic_orphaned_interfaces_deleted_total",
			Help: "Total number of orphaned interfaces deleted",
		},
	)

	// 에러 메트릭
	ErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "multinic_errors_total",
			Help: "Total number of errors encountered",
		},
		[]string{"error_type"}, // validation, network, system, not_found
	)

	// 시스템 정보
	AgentInfo = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "multinic_agent_info",
			Help: "Agent information",
		},
		[]string{"version", "os_type", "node_name"},
	)
)

// RecordInterfaceProcessing은 인터페이스 처리 시간을 기록합니다
func RecordInterfaceProcessing(interfaceName string, status string, duration float64) {
	InterfaceProcessingDuration.WithLabelValues(interfaceName, status).Observe(duration)
	InterfacesProcessed.WithLabelValues(status).Inc()
}

// RecordPollingCycle은 폴링 사이클 메트릭을 기록합니다
func RecordPollingCycle(duration float64) {
	PollingCycleCount.Inc()
	PollingCycleDuration.Observe(duration)
}

// RecordDBQuery는 데이터베이스 쿼리 시간을 기록합니다
func RecordDBQuery(queryType string, duration float64) {
	DBQueryDuration.WithLabelValues(queryType).Observe(duration)
}

// RecordError는 에러 발생을 기록합니다
func RecordError(errorType string) {
	ErrorsTotal.WithLabelValues(errorType).Inc()
}

// RecordDrift는 설정 드리프트를 기록합니다
func RecordDrift(driftType string) {
	ConfigurationDrifts.WithLabelValues(driftType).Inc()
}

// SetConcurrentTasks는 현재 동시 처리 중인 작업 수를 설정합니다
func SetConcurrentTasks(count float64) {
		ConcurrentTasks.Set(count)
}

// Worker 메트릭: 편의 헬퍼
func SetWorkerQueueDepth(pool string, depth int) {
    WorkerQueueDepth.WithLabelValues(pool).Set(float64(depth))
}

func SetWorkerActive(pool string, active, total int) {
    WorkerActive.WithLabelValues(pool).Set(float64(active))
    if total > 0 {
        WorkerUtilization.WithLabelValues(pool).Set(float64(active) / float64(total))
    }
}

func ObserveWorkerTask(pool, status string, durationSeconds float64) {
    WorkerTaskDuration.WithLabelValues(pool, status).Observe(durationSeconds)
}

func IncWorkerRetry(pool string) {
    WorkerRetries.WithLabelValues(pool).Inc()
}

func IncWorkerPanic(pool string) {
    WorkerPanics.WithLabelValues(pool).Inc()
}

// SetBackoffLevel은 현재 백오프 레벨을 설정합니다
func SetBackoffLevel(level float64) {
	PollingBackoffLevel.Set(level)
}

// SetDBConnectionStatus는 데이터베이스 연결 상태를 설정합니다
func SetDBConnectionStatus(connected bool) {
	if connected {
		DBConnectionStatus.Set(1)
	} else {
		DBConnectionStatus.Set(0)
	}
}

// SetAgentInfo는 에이전트 정보를 설정합니다
func SetAgentInfo(version, osType, nodeName string) {
	AgentInfo.WithLabelValues(version, osType, nodeName).Set(1)
}
