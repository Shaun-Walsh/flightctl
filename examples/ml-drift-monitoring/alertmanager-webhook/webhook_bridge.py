# Created by Shaun Walsh
"""Translates Alertmanager webhooks to GitHub Actions repository_dispatch."""

import os
import requests
from flask import Flask, request, jsonify

app = Flask(__name__)

GITHUB_TOKEN = os.environ["GITHUB_TOKEN"]
GITHUB_REPO = os.environ.get("GITHUB_REPO", "Shaun-Walsh/flightctl")


@app.route("/webhook", methods=["POST"])
def webhook():
    for alert in request.json.get("alerts", []):
        if alert.get("status") != "firing":
            continue
        labels = alert.get("labels", {})
        if "MLModel" not in labels.get("alertname", ""):
            continue
        requests.post(
            f"https://api.github.com/repos/{GITHUB_REPO}/dispatches",
            headers={
                "Accept": "application/vnd.github+json",
                "Authorization": f"Bearer {GITHUB_TOKEN}",
            },
            json={
                "event_type": "ml-drift-detected",
                "client_payload": {
                    "alert": labels.get("alertname"),
                    "device": labels.get("resource"),
                    "summary": alert.get("annotations", {}).get("summary", ""),
                },
            },
        )
        app.logger.info("Dispatched %s to GitHub Actions", labels["alertname"])
    return jsonify({"status": "ok"}), 200


@app.route("/health")
def health():
    return jsonify({"status": "ok"}), 200


if __name__ == "__main__":
    app.run(host="0.0.0.0", port=9095)
