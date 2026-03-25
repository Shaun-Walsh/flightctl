# Created by Shaun Walsh
FROM python:3.11-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY webhook_bridge.py .
CMD ["python", "webhook_bridge.py"]
