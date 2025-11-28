package resource

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestMLModelMonitor(t *testing.T) {
	require := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create mock ML metrics endpoint
	mockServer := newMockMLMetricsServer(t, mockMLMetrics{
		DriftScore: 0.75, // 75% drift
	})
	defer mockServer.Close()

	log := log.NewPrefixLogger("test")
	mlModelMonitor := NewMLModelMonitor(log)

	go mlModelMonitor.Run(ctx)

	samplingInterval := 100 * time.Millisecond
	monitorSpec := v1alpha1.MLModelResourceMonitorSpec{
		SamplingInterval: samplingInterval.String(),
		MonitorType:      MLModelMonitorType,
		SidecarEndpoint:  lo.ToPtr(mockServer.URL + "/metrics"),
		AlertRules: []v1alpha1.ResourceAlertRule{
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeCritical,
				Percentage:  90, // 90% drift should never fire
				Duration:    "90ms",
				Description: "Critical: ML model drift is above 90% for 90ms",
			},
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeWarning,
				Percentage:  70, // 70% drift should fire (75% > 70%)
				Duration:    "90ms",
				Description: "Warning: ML model drift is above 70% for 90ms",
			},
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeInfo,
				Percentage:  70, // 70% drift should never fire because of duration
				Duration:    "1h",
				Description: "Info: ML model drift is above 70% for 1h",
			},
		},
	}

	rm := &v1alpha1.ResourceMonitor{}
	err := rm.FromMLModelResourceMonitorSpec(monitorSpec)
	require.NoError(err)

	updated, err := mlModelMonitor.Update(rm)
	require.NoError(err)
	require.True(updated)

	var alerts []v1alpha1.ResourceAlertRule

	require.Eventually(func() bool {
		alerts = mlModelMonitor.Alerts()
		return len(alerts) == 1
	}, retryTimeout, retryInterval, "alert add")

	deviceResourceStatusType, alertMsg := getHighestSeverityResourceStatusFromAlerts(MLModelMonitorType, alerts)
	require.NotEmpty(alertMsg) // ensure we have an alert message

	require.Equal(v1alpha1.DeviceResourceStatusWarning, deviceResourceStatusType)

	// update the monitor to remove all alerts
	monitorSpec.AlertRules = monitorSpec.AlertRules[:0]
	rm = &v1alpha1.ResourceMonitor{}
	err = rm.FromMLModelResourceMonitorSpec(monitorSpec)
	require.NoError(err)

	updated, err = mlModelMonitor.Update(rm)
	require.NoError(err)
	require.True(updated)

	// ensure no alerts after clearing
	require.Eventually(func() bool {
		alerts := mlModelMonitor.Alerts()
		return len(alerts) == 0
	}, retryTimeout, retryInterval, "alerts remove")
}

func TestConvertDriftToPercentage(t *testing.T) {
	tests := []struct {
		name       string
		driftScore float64
		expected   int64
	}{
		{
			name:       "normal drift 0.5",
			driftScore: 0.5,
			expected:   50,
		},
		{
			name:       "zero drift",
			driftScore: 0.0,
			expected:   0,
		},
		{
			name:       "max drift 1.0",
			driftScore: 1.0,
			expected:   100,
		},
		{
			name:       "negative drift clamped to 0",
			driftScore: -0.5,
			expected:   0,
		},
		{
			name:       "drift above 1.0 clamped to 100",
			driftScore: 1.5,
			expected:   100,
		},
		{
			name:       "small drift rounds correctly",
			driftScore: 0.004,
			expected:   0,
		},
		{
			name:       "drift rounds up at 0.5",
			driftScore: 0.005,
			expected:   1,
		},
		{
			name:       "drift 0.764 rounds to 76",
			driftScore: 0.764,
			expected:   76,
		},
		{
			name:       "drift 0.765 rounds to 77",
			driftScore: 0.765,
			expected:   77,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertDriftToPercentage(tt.driftScore)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestMLModelCollectorHTTPErrors(t *testing.T) {
	tests := []struct {
		name          string
		serverHandler http.HandlerFunc
		expectError   bool
		errorContains string
	}{
		{
			name: "successful collection with all fields",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"drift_score":     0.85,
					"confidence_avg":  0.91,
					"prediction_rate": 5000,
					"model_version":   "v2.0.0",
				})
			},
			expectError: false,
		},
		{
			name: "successful collection with required field only",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"drift_score": 0.42,
				})
			},
			expectError: false,
		},
		{
			name: "missing drift_score field",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"confidence_avg": 0.91,
				})
			},
			expectError:   true,
			errorContains: "drift_score field missing",
		},
		{
			name: "invalid JSON response",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte("invalid json"))
			},
			expectError:   true,
			errorContains: "failed to parse JSON response",
		},
		{
			name: "HTTP 500 error",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			expectError:   true,
			errorContains: "sidecar returned non-OK status: 500",
		},
		{
			name: "HTTP 404 error",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			expectError:   true,
			errorContains: "sidecar returned non-OK status: 404",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			mockServer := httptest.NewServer(tt.serverHandler)
			defer mockServer.Close()

			log := log.NewPrefixLogger("test")
			collector := newMLModelCollector(mockServer.URL, log)

			usage := &MLModelUsage{}
			err := collector.CollectUsage(context.Background(), usage)

			if tt.expectError {
				require.Error(err)
				if tt.errorContains != "" {
					require.Contains(err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(err)
				require.NotNil(usage)
			}
		})
	}
}

// mockMLMetrics represents metrics returned by mock ML monitoring sidecar
type mockMLMetrics struct {
	DriftScore float64
}

// newMockMLMetricsServer creates a mock HTTP server that returns ML metrics
func newMockMLMetricsServer(t *testing.T, metrics mockMLMetrics) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		response := map[string]interface{}{
			"drift_score": metrics.DriftScore,
		}

		json.NewEncoder(w).Encode(response)
	}))
}
