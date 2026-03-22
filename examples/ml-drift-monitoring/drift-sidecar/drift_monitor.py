#!/usr/bin/env python3
# Created by Shaun Walsh
"""ML Model Drift Monitor - Scrapes llama.cpp metrics and uses Evidently AI
to detect operational drift in inference server performance."""

import json
import logging
import requests
import pandas as pd
from flask import Flask, jsonify, request
from datetime import datetime
from evidently.test_suite import TestSuite
from evidently.tests import TestColumnDrift

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

app = Flask(__name__)

INFERENCE_METRICS_URL = 'http://host.containers.internal:8000/metrics'

# Load reference data — baseline performance captured from healthy server
with open('/app/reference_data.json', 'r') as f:
    ref_data = pd.DataFrame(json.load(f)['samples'])

# Rolling window of scraped metrics
current_samples = []
prev_tokens_total = 0
simulated_drift = None  # Override drift score for demo purposes


def scrape_llamacpp():
    """Scrape Prometheus metrics from llama.cpp inference server.
    Returns dict of metric values or None on failure."""
    try:
        resp = requests.get(INFERENCE_METRICS_URL, timeout=3)
        metrics = {}
        for line in resp.text.split('\n'):
            if line.startswith('#') or not line.strip():
                continue
            parts = line.split()
            if len(parts) == 2:
                metrics[parts[0]] = float(parts[1])
        return metrics
    except Exception as e:
        logger.warning("Failed to scrape llama.cpp: %s", e)
        return None


def calculate_drift():
    """Scrape llama.cpp and calculate drift using Evidently."""
    global prev_tokens_total, current_samples

    raw = scrape_llamacpp()
    if raw is None:
        return 0.0, 0.0, 0

    # Extract metrics from llama.cpp Prometheus output
    gen_throughput = raw.get('llamacpp:predicted_tokens_seconds', 0.0)
    prompt_throughput = raw.get('llamacpp:prompt_tokens_seconds', 0.0)
    tokens_total = raw.get('llamacpp:tokens_predicted_total', 0.0)

    # Calculate prediction rate (tokens generated since last scrape)
    rate = int(tokens_total - prev_tokens_total)
    prev_tokens_total = tokens_total

    # Only record a sample if the server has processed requests
    if gen_throughput > 0:
        current_samples.append({
            'gen_throughput': gen_throughput,
            'prompt_throughput': prompt_throughput,
        })

    # Keep rolling window of last 100 samples
    if len(current_samples) > 100:
        current_samples.pop(0)

    # Need minimum samples for statistical comparison
    if len(current_samples) < 5:
        return 0.0, gen_throughput, rate

    # Run Evidently drift test on throughput metrics
    current_df = pd.DataFrame(current_samples)

    suite = TestSuite(tests=[
        TestColumnDrift(column_name='gen_throughput'),
        TestColumnDrift(column_name='prompt_throughput'),
    ])

    suite.run(reference_data=ref_data, current_data=current_df)
    results = suite.as_dict()

    # Extract drift scores from test results
    drift_scores = []
    for test in results.get('tests', []):
        if 'drift_score' in test.get('parameters', {}):
            drift_scores.append(test['parameters']['drift_score'])

    drift = sum(drift_scores) / len(drift_scores) if drift_scores else 0.0

    return round(drift, 4), round(gen_throughput, 2), rate


@app.route('/simulate', methods=['POST'])
def simulate():
    """Override drift score for demo. POST with ?drift_score=0.5 or reset with ?reset=true"""
    global simulated_drift
    if request.args.get('reset'):
        simulated_drift = None
        logger.info("Simulation reset — returning to live metrics")
        return jsonify({'status': 'reset', 'simulated_drift': None})
    score = request.args.get('drift_score', type=float)
    if score is None:
        return jsonify({'error': 'drift_score parameter required'}), 400
    simulated_drift = max(0.0, min(1.0, score))
    logger.info("Simulating drift_score=%.2f", simulated_drift)
    return jsonify({'status': 'simulating', 'simulated_drift': simulated_drift})


@app.route('/health')
def health():
    return jsonify({'status': 'ok'}), 200


@app.route('/metrics')
def metrics():
    drift, throughput, rate = calculate_drift()
    if simulated_drift is not None:
        drift = simulated_drift

    return jsonify({
        'drift_score': drift,
        'confidence_avg': throughput,
        'prediction_rate': rate,
        'model_version': 'Llama-3.2-1B-Instruct',
        'last_updated': datetime.utcnow().isoformat() + 'Z'
    })


if __name__ == '__main__':
    logger.info("Starting drift monitor — scraping llama.cpp at %s",
                INFERENCE_METRICS_URL)
    app.run(host='0.0.0.0', port=5001)
