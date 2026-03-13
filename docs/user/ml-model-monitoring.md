<!-- Created by Shaun Walsh -->
# ML Model Monitoring

Flight Control monitors ML model drift and performance by polling external ML monitoring sidecars. The agent collects drift metrics from your ML monitoring tool and raises alerts when drift exceeds configured thresholds.

## Overview

ML model drift occurs when the statistical properties of input data change over time, degrading model accuracy. Flight Control integrates with ML monitoring tools (such as Evidently AI, NannyML, or custom solutions) to track drift and notify operators when models require retraining or investigation.

The agent polls the monitoring sidecar's HTTP endpoint at regular intervals and evaluates drift scores against alert rules. When drift exceeds a threshold for the specified duration, Flight Control generates alerts through the standard alerting system.

## Configuration

ML model monitors are configured in the device or fleet specification under `spec.resourceMonitors`:

```yaml
apiVersion: flightctl.io/v1alpha1
kind: Device
metadata:
  name: edge-device-01
spec:
  resourceMonitors:
    - monitorType: MLModel
      sidecarEndpoint: "http://localhost:5001/metrics"
      modelName: "fraud-detection-v2"
      samplingInterval: "5m"
      alertRules:
        - severity: Warning
          percentage: 15
          duration: "10m"
          description: "Model drift above 15% for 10 minutes"
        - severity: Critical
          percentage: 25
          duration: "5m"
          description: "Model drift above 25% for 5 minutes"
```

### Parameters

| Parameter | Type | Required | Description |
| --------- | ---- | :------: | ----------- |
| `monitorType` | `string` | Y | Must be `MLModel` |
| `sidecarEndpoint` | `string` | | HTTP endpoint of the ML monitoring sidecar. Default: `http://localhost:5001/metrics` |
| `modelName` | `string` | | Name of the ML model being monitored. Used for identification in alerts and logs. |
| `samplingInterval` | `string` | Y | Duration between drift checks. Format: positive integer followed by 's' (seconds), 'm' (minutes), or 'h' (hours). Example: `5m`, `30s`, `1h` |
| `alertRules` | `array` | Y | Alert rules defining drift thresholds and durations. See [Alert Rules](#alert-rules). |

### Alert Rules

Alert rules define when the agent generates alerts based on drift percentage and observation duration.

| Parameter | Type | Required | Description |
| --------- | ---- | :------: | ----------- |
| `severity` | `string` | Y | Alert severity level: `Info`, `Warning`, or `Critical` |
| `percentage` | `integer` | Y | Drift threshold as percentage (0-100). Alert fires when drift exceeds this value. |
| `duration` | `string` | Y | Time the drift must remain above threshold before firing. Format: `30s`, `5m`, `1h` |
| `description` | `string` | | Human-readable description of the alert condition |

> [!NOTE]
> Only one alert rule per severity level is allowed. The agent uses the highest severity alert that is currently firing.

## Sidecar Integration

The ML monitoring sidecar must expose an HTTP endpoint that returns drift metrics in JSON format.

### Endpoint Requirements

- **Method**: GET
- **Path**: Configurable via `sidecarEndpoint`
- **Response**: JSON with drift score

### Response Format

```json
{
  "drift_score": 0.23,
  "confidence_avg": 0.87,
  "prediction_rate": 1250,
  "model_version": "fraud-detection-v2.1.3"
}
```

| Field | Type | Required | Description |
| ----- | ---- | :------: | ----------- |
| `drift_score` | `float` | Y | Drift score between 0.0 and 1.0 representing the degree of drift detected |
| `confidence_avg` | `float` | | Average model confidence score across recent predictions |
| `prediction_rate` | `integer` | | Number of predictions per second |
| `model_version` | `string` | | Version identifier of the deployed model |

The agent converts `drift_score` to a percentage (0.0 → 0%, 1.0 → 100%) and evaluates it against the configured alert rules.

## Example Configurations

### Evidently AI Sidecar

```yaml
apiVersion: flightctl.io/v1alpha1
kind: Fleet
metadata:
  name: ml-inference-fleet
spec:
  resourceMonitors:
    - monitorType: MLModel
      sidecarEndpoint: "http://localhost:8085/metrics"
      modelName: "recommendation-engine"
      samplingInterval: "10m"
      alertRules:
        - severity: Warning
          percentage: 10
          duration: "30m"
        - severity: Critical
          percentage: 20
          duration: "15m"
```

### Custom Monitoring Solution

```yaml
spec:
  resourceMonitors:
    - monitorType: MLModel
      sidecarEndpoint: "http://ml-monitor:9090/drift"
      modelName: "sentiment-analysis"
      samplingInterval: "2m"
      alertRules:
        - severity: Info
          percentage: 5
          duration: "1h"
        - severity: Warning
          percentage: 12
          duration: "20m"
        - severity: Critical
          percentage: 18
          duration: "10m"
```

### Multiple Models

Monitor multiple models by configuring separate resource monitors:

```yaml
spec:
  resourceMonitors:
    - monitorType: MLModel
      sidecarEndpoint: "http://localhost:5001/metrics"
      modelName: "product-classifier"
      samplingInterval: "5m"
      alertRules:
        - severity: Warning
          percentage: 15
          duration: "10m"

    - monitorType: MLModel
      sidecarEndpoint: "http://localhost:5002/metrics"
      modelName: "fraud-detector"
      samplingInterval: "1m"
      alertRules:
        - severity: Critical
          percentage: 10
          duration: "5m"
```

## Alerts

When drift exceeds a threshold for the configured duration, Flight Control generates alerts visible in the device status and forwarded to Alertmanager.

### Alert Labels

ML model drift alerts include the following labels:

- `org_id`: Organization identifier
- `resource`: Device name
- `severity`: Alert severity (Info, Warning, Critical)
- `monitor_type`: `MLModel`
- `model_name`: Name from configuration

### Device Status

Check ML model monitoring status using the CLI:

```bash
flightctl get device edge-device-01 -o yaml
```

The device status shows the current drift state under `status.resources.mlmodel`:

```yaml
status:
  resources:
    mlmodel: Warning
  summary:
    status: Degraded
    info: "Degraded resource alert: MLModel"
```

## Troubleshooting

### Sidecar Endpoint Not Reachable

If the agent cannot reach the sidecar endpoint:

```bash
# Check agent logs
journalctl -u flightctl-agent | grep -i mlmodel

# Verify sidecar is running
curl http://localhost:5001/metrics

# Check network connectivity from agent
curl http://ml-monitor:9090/drift
```

Common causes:
- Sidecar not started or crashed
- Incorrect endpoint URL in configuration
- Network policy blocking agent-to-sidecar communication
- Port not exposed in container/pod configuration

### Invalid JSON Response

The agent logs an error if the sidecar returns malformed JSON:

```
failed to parse JSON response from sidecar
```

Verify the sidecar response format:

```bash
curl http://localhost:5001/metrics | jq '.'
```

Ensure the response contains the required `drift_score` field as a number between 0.0 and 1.0.

### Missing drift_score Field

If the sidecar response lacks `drift_score`:

```
drift_score field missing from sidecar response
```

Update your sidecar implementation to include this required field in the JSON response.

### HTTP Error Codes

The agent handles HTTP errors from the sidecar:

- **404 Not Found**: Check the endpoint path in `sidecarEndpoint`
- **500 Internal Server Error**: Check sidecar logs for errors
- **Connection refused**: Verify sidecar is running and port is correct

Enable debug logging to see detailed HTTP error messages:

```yaml
# /etc/flightctl/config.yaml
log-level: debug
```

### Alerts Not Firing

If drift is high but alerts do not fire:

1. **Check duration**: Alert only fires after drift remains above threshold for the configured duration
2. **Verify percentage**: Ensure drift percentage exceeds the configured threshold
3. **Check drift calculation**: Agent converts drift_score to percentage (0.75 → 75%)

View current drift metrics in device status:

```bash
flightctl get device edge-device-01 -o json | jq '.status.resources.mlmodel'
```

## Best Practices

### Sampling Interval

- **Real-time models**: Use shorter intervals (1-5 minutes)
- **Batch models**: Use longer intervals (10-30 minutes)
- Balance monitoring frequency with sidecar overhead

### Alert Thresholds

Configure thresholds based on your model's sensitivity:

- **High-stakes models** (fraud, medical): Lower thresholds (10-15%)
- **Recommendation engines**: Higher thresholds (20-30%)
- **Experimental models**: Info alerts at 5%, warnings at 15%

### Duration Windows

Use duration windows to avoid alert fatigue from transient spikes:

- **Critical alerts**: 5-15 minutes (quick response to severe drift)
- **Warning alerts**: 15-30 minutes (actionable drift trends)
- **Info alerts**: 1-2 hours (awareness of gradual drift)

### Sidecar Deployment

Deploy ML monitoring sidecars as:
- **Systemd services**: For bare-metal or VM deployments
- **Sidecar containers**: In Kubernetes pods alongside inference containers
- **Separate services**: On localhost or networked endpoints

Ensure the sidecar has access to:
- Reference dataset for drift calculation
- Recent prediction inputs or outputs
- Model metadata and versioning information
