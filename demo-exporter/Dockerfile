FROM python:3.13-slim
WORKDIR /app
RUN pip install --no-cache-dir prometheus_client==0.26.0
COPY exporter.py .
EXPOSE 9491
CMD ["python", "exporter.py"]
