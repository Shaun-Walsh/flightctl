package resource

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	DefaultMLModelSyncTimeout      = 5 * time.Second
	DefaultMLModelSidecarEndpoint  = "http://localhost:5001/metrics"
	DefaultMLModelHTTPClientTimeout = 3 * time.Second
)

var _ Monitor[MLModelUsage] = (*MLModelMonitor)(nil)

type MLModelMonitor struct {
	mu       sync.Mutex
	alerts   map[v1alpha1.ResourceAlertSeverityType]*Alert
	endpoint string

	updateIntervalCh chan time.Duration
	samplingInterval time.Duration
	collector        Collector[MLModelUsage]

	log *log.PrefixLogger
}

func NewMLModelMonitor(
	log *log.PrefixLogger,
) *MLModelMonitor {
	return &MLModelMonitor{
		alerts:           make(map[v1alpha1.ResourceAlertSeverityType]*Alert),
		updateIntervalCh: make(chan time.Duration, 1),
		samplingInterval: DefaultSamplingInterval,
		endpoint:         DefaultMLModelSidecarEndpoint,
		collector:        newMLModelCollector(DefaultMLModelSidecarEndpoint, log),
		log:              log,
	}
}

func (m *MLModelMonitor) Run(ctx context.Context) {
	defer m.log.Infof("ML model monitor stopped")
	samplingInterval := m.getSamplingInterval()
	ticker := time.NewTicker(samplingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case newInterval := <-m.updateIntervalCh:
			ticker.Reset(newInterval)
		case <-ticker.C:
			m.log.Debug("Checking ML model drift")
			usage := MLModelUsage{}
			m.sync(ctx, &usage)
		}
	}
}

func (m *MLModelMonitor) Update(monitor *v1alpha1.ResourceMonitor) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	spec, err := monitor.AsMLModelResourceMonitorSpec()
	if err != nil {
		return false, err
	}

	updated, err := updateMonitorML(m.log, &spec, &m.samplingInterval, m.alerts, m.updateIntervalCh)
	if err != nil {
		return updated, err
	}

	newEndpoint := DefaultMLModelSidecarEndpoint
	if spec.SidecarEndpoint != nil && *spec.SidecarEndpoint != "" {
		newEndpoint = *spec.SidecarEndpoint
	}

	if newEndpoint != m.endpoint {
		m.endpoint = newEndpoint
		m.collector = newMLModelCollector(newEndpoint, m.log)
		updated = true
	}

	return updated, nil
}

func (m *MLModelMonitor) Alerts() []v1alpha1.ResourceAlertRule {
	m.mu.Lock()
	defer m.mu.Unlock()
	var firing []v1alpha1.ResourceAlertRule
	for _, alert := range m.alerts {
		if alert.IsFiring() {
			firing = append(firing, alert.ResourceAlertRule)
		}
	}
	return firing
}

func (m *MLModelMonitor) sync(ctx context.Context, usage *MLModelUsage) {
	if !m.hasAlertRules() {
		m.log.Debug("Skipping ML model drift sync: no alert rules")
		return
	}

	ctx, cancel := context.WithTimeout(ctx, DefaultMLModelSyncTimeout)
	defer cancel()

	endpoint := m.getEndpoint()
	if err := m.collector.CollectUsage(ctx, usage); err != nil {
		m.log.Errorf("Failed to collect ML model metrics from endpoint %s: %v", endpoint, err)
		return
	}

	m.ensureAlerts(usage.UsedPercent)
}

func (m *MLModelMonitor) ensureAlerts(percentageUsed int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Tracef("ML model drift: %d%%", percentageUsed)
	for _, alert := range m.alerts {
		alert.Sync(percentageUsed)
	}
}

func (m *MLModelMonitor) hasAlertRules() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.alerts) > 0
}

func (m *MLModelMonitor) getSamplingInterval() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.samplingInterval
}

func (m *MLModelMonitor) getEndpoint() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.endpoint
}

// updateMonitorML is a specialized version of updateMonitor for ML model monitors
func updateMonitorML(
	log *log.PrefixLogger,
	spec *v1alpha1.MLModelResourceMonitorSpec,
	currentSampleInterval *time.Duration,
	alerts map[v1alpha1.ResourceAlertSeverityType]*Alert,
	updateIntervalCh chan time.Duration,
) (bool, error) {
	newSamplingInterval, err := time.ParseDuration(spec.SamplingInterval)
	if err != nil {
		return false, err
	}

	updated, err := updateAlerts(spec.AlertRules, alerts)
	if err != nil {
		return updated, err
	}

	if *currentSampleInterval != newSamplingInterval {
		log.Infof("Updating sampling interval from %s to %s", *currentSampleInterval, newSamplingInterval)
		updateIntervalCh <- newSamplingInterval
		*currentSampleInterval = newSamplingInterval
		updated = true
	}

	return updated, nil
}

// MLModelUsage represents the tracked ML model drift and performance metrics.
type MLModelUsage struct {
	DriftScore     float64 // 0.0-1.0 from ML monitoring sidecar
	ConfidenceAvg  float64 // Average prediction confidence
	PredictionRate int64   // Predictions per sampling interval
	ModelVersion   string  // Current model version (if provided)

	UsedPercent     int64 // DriftScore * 100 for alert compatibility
	lastCollectedAt time.Time
	err             error
}

// Error returns any error that occurred during collection.
func (u *MLModelUsage) Error() error {
	return u.err
}

type mlModelCollector struct {
	endpoint   string
	httpClient *http.Client
	log        *log.PrefixLogger
}

func newMLModelCollector(endpoint string, log *log.PrefixLogger) Collector[MLModelUsage] {
	return &mlModelCollector{
		endpoint: endpoint,
		httpClient: &http.Client{
			Timeout: DefaultMLModelHTTPClientTimeout,
		},
		log: log,
	}
}

func (c *mlModelCollector) CollectUsage(ctx context.Context, usage *MLModelUsage) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
		if err != nil {
			return fmt.Errorf("failed to create HTTP request: %w", err)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("failed to fetch metrics from sidecar: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("sidecar returned non-OK status: %d", resp.StatusCode)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read response body: %w", err)
		}

		var metrics struct {
			DriftScore     *float64 `json:"drift_score"`
			ConfidenceAvg  *float64 `json:"confidence_avg"`
			PredictionRate *int64   `json:"prediction_rate"`
			ModelVersion   *string  `json:"model_version"`
		}

		if err := json.Unmarshal(body, &metrics); err != nil {
			return fmt.Errorf("failed to parse JSON response: %w", err)
		}

		// DriftScore is the critical metric; others are optional
		if metrics.DriftScore == nil {
			return fmt.Errorf("drift_score field missing from sidecar response")
		}

		usage.DriftScore = *metrics.DriftScore
		usage.UsedPercent = convertDriftToPercentage(usage.DriftScore)

		if metrics.ConfidenceAvg != nil {
			usage.ConfidenceAvg = *metrics.ConfidenceAvg
		}
		if metrics.PredictionRate != nil {
			usage.PredictionRate = *metrics.PredictionRate
		}
		if metrics.ModelVersion != nil {
			usage.ModelVersion = *metrics.ModelVersion
		}

		usage.lastCollectedAt = time.Now()
		return nil
	}
}

// convertDriftToPercentage converts drift score (0.0-1.0) to percentage (0-100)
// for compatibility with the existing alert infrastructure.
func convertDriftToPercentage(driftScore float64) int64 {
	if driftScore < 0 {
		return 0
	}
	if driftScore > 1 {
		return 100
	}
	return int64(math.Round(driftScore * 100))
}
