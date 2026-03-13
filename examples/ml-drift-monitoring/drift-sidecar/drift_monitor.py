#!/usr/bin/env python3
# Created by Shaun Walsh
"""ML Model Drift Monitor - Uses Evidently AI to detect drift"""

import os
import json
import logging
import requests
import numpy as np
import pandas as pd
from flask import Flask, jsonify
from datetime import datetime
from evidently.test_suite import TestSuite
from evidently.tests import TestColumnDrift

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

app = Flask(__name__)

# Load reference data
with open('/app/reference_data.json', 'r') as f:
    ref_data = pd.DataFrame(json.load(f)['samples'])

# Current data window
current_samples = []
request_count = 0

def scrape_vllm():
    """Get request count from vLLM metrics"""
    try:
        resp = requests.get('http://localhost:8000/metrics', timeout=2)
        for line in resp.text.split('\n'):
            if 'vllm:request_success_total' in line and not line.startswith('#'):
                return int(float(line.split()[-1]))
    except:
        pass
    return 0

def calculate_drift():
    """Calculate drift using Evidently"""
    global request_count, current_samples

    # Get current metrics
    new_count = scrape_vllm()
    rate = new_count - request_count
    request_count = new_count

    # Add synthetic current sample (in production: actual inference data)
    current_samples.append({
        'latency': np.random.normal(160, 35),  # Slightly higher than reference
        'confidence': np.random.beta(7, 2.5),  # Slightly lower than reference
    })

    # Keep last 100 samples
    if len(current_samples) > 100:
        current_samples.pop(0)

    # Need enough samples for drift detection
    if len(current_samples) < 20:
        return 0.0, 1.0, rate

    # Run Evidently drift test (following official Evidently AI pattern)
    # Reference: https://docs.evidentlyai.com/user-guide/tests-and-reports/run-tests
    current_df = pd.DataFrame(current_samples)

    suite = TestSuite(tests=[
        TestColumnDrift(column_name='latency'),
        TestColumnDrift(column_name='confidence'),
    ])

    suite.run(reference_data=ref_data, current_data=current_df)
    results = suite.as_dict()

    # Extract drift scores from test results
    drift_scores = []
    for test in results.get('tests', []):
        if 'drift_score' in test.get('parameters', {}):
            drift_scores.append(test['parameters']['drift_score'])

    drift = sum(drift_scores) / len(drift_scores) if drift_scores else 0.0
    confidence = current_df['confidence'].mean()

    return round(drift, 4), round(confidence, 4), rate

@app.route('/health')
def health():
    return jsonify({'status': 'ok'}), 200

@app.route('/metrics')
def metrics():
    drift, confidence, rate = calculate_drift()

    return jsonify({
        'drift_score': drift,
        'confidence_avg': confidence,
        'prediction_rate': rate,
        'model_version': 'Llama-3.2-1B-Instruct',
        'last_updated': datetime.utcnow().isoformat() + 'Z'
    })

if __name__ == '__main__':
    logger.info("Starting drift monitor with Evidently AI")
    app.run(host='0.0.0.0', port=5001)
