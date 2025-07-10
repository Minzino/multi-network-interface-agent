package health

import (
	"encoding/json"
	"fmt"
	"multinic-agent/internal/domain/interfaces"
	"net/http"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// HealthService provides health check functionality
type HealthService struct {
	mu             sync.RWMutex
	clock          interfaces.Clock
	logger         *logrus.Logger
	startTime      time.Time
	dbHealthy      bool
	dbError        error
	processedVMs   int64
	failedConfigs  int64
	networkManager string
}

// HealthStatus represents health check status
type HealthStatus string

const (
	StatusHealthy   HealthStatus = "healthy"
	StatusDegraded  HealthStatus = "degraded"
	StatusUnhealthy HealthStatus = "unhealthy"
)

// HealthResponse is the health check response struct
type HealthResponse struct {
	Status     HealthStatus           `json:"status"`
	Timestamp  string                 `json:"timestamp"`
	LastCheck  string                 `json:"last_check"`
	Components map[string]interface{} `json:"components"`
	Statistics map[string]interface{} `json:"statistics"`
}

// NewHealthService creates a new HealthService
func NewHealthService(clock interfaces.Clock, logger *logrus.Logger) *HealthService {
	return &HealthService{
		clock:     clock,
		logger:    logger,
		startTime: clock.Now(),
		dbHealthy: false,
	}
}

// UpdateDBHealth updates the database health status
func (h *HealthService) UpdateDBHealth(healthy bool, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.dbHealthy = healthy
	h.dbError = err
}

// IncrementProcessedVMs increments the processed VM count
func (h *HealthService) IncrementProcessedVMs() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.processedVMs++
}

// IncrementFailedConfigs increments the failed configuration count
func (h *HealthService) IncrementFailedConfigs() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.failedConfigs++
}

// SetNetworkManager sets the network manager type in use
func (h *HealthService) SetNetworkManager(managerType string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.networkManager = managerType
}

// ServeHTTP handles the HTTP health check endpoint
func (h *HealthService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	response := h.buildHealthResponse()

	// Set HTTP status code based on health status
	statusCode := http.StatusOK
	if response.Status == StatusUnhealthy {
		statusCode = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.logger.WithError(err).Error("failed to encode health check response")
	}
}

// buildHealthResponse constructs the health check response
func (h *HealthService) buildHealthResponse() HealthResponse {
	h.mu.RLock()
	defer h.mu.RUnlock()

	now := h.clock.Now()

	// Determine overall status
	status := h.determineOverallStatus()

	// Component status
	components := map[string]interface{}{
		"database": map[string]interface{}{
			"healthy": h.dbHealthy,
			"error":   h.formatError(h.dbError),
		},
		"network_manager": map[string]interface{}{
			"type": h.networkManager,
		},
	}

	// Statistics information
	statistics := map[string]interface{}{
		"processed_vms":  h.processedVMs,
		"failed_configs": h.failedConfigs,
		"uptime":         h.formatUptime(now.Sub(h.startTime)),
	}

	return HealthResponse{
		Status:     status,
		Timestamp:  now.Format(time.RFC3339),
		LastCheck:  now.Format(time.RFC3339),
		Components: components,
		Statistics: statistics,
	}
}

// determineOverallStatus determines the overall health status
func (h *HealthService) determineOverallStatus() HealthStatus {
	// If database is unhealthy, overall status is unhealthy
	if !h.dbHealthy {
		return StatusUnhealthy
	}

	// If failed configurations are 50% or more, status is degraded
	if h.processedVMs > 0 && h.failedConfigs > 0 {
		failureRate := float64(h.failedConfigs) / float64(h.processedVMs+h.failedConfigs)
		if failureRate >= 0.5 {
			return StatusDegraded
		}
	}

	return StatusHealthy
}

// formatError formats an error to string
func (h *HealthService) formatError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// formatUptime formats uptime duration to human-readable format
func (h *HealthService) formatUptime(duration time.Duration) string {
	days := int(duration.Hours()) / 24
	hours := int(duration.Hours()) % 24
	minutes := int(duration.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd%dh%dm", days, hours, minutes)
	} else if hours > 0 {
		return fmt.Sprintf("%dh%dm", hours, minutes)
	} else {
		return fmt.Sprintf("%dm", minutes)
	}
}
