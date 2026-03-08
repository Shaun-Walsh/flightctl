FROM registry.access.redhat.com/ubi9/python-311:latest

USER root

# Install dependencies
COPY requirements.txt /app/
RUN pip install --no-cache-dir -r /app/requirements.txt

# Copy application files
COPY drift_monitor.py /app/
COPY reference_data.json /app/

WORKDIR /app

# Run as non-root user
USER 1001

# Expose metrics endpoint
EXPOSE 5001

# Start drift monitor
CMD ["python", "drift_monitor.py"]
