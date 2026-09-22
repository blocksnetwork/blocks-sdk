FROM python:3.12-slim

# Security updates the base tag predates. On a warm cache, pass --build-arg SECURITY_REFRESH=$(date +%F) to re-run this.
ARG SECURITY_REFRESH=initial
RUN echo "security-refresh ${SECURITY_REFRESH}" \
 && apt-get update \
 && apt-get upgrade -y --no-install-recommends \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY pyproject.toml ./
RUN pip install --no-cache-dir .
COPY . .
CMD ["python", "run.py"]
