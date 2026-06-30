# Stage 1: Builder
FROM python:3.11-slim AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    g++ \
    libpq-dev \
    libmagic1 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build

# requirements.txt exported from poetry.lock via:
#   poetry export -f requirements.txt --only main --without-hashes -o requirements.txt
COPY requirements.txt .

RUN pip install --no-cache-dir --prefix=/install -r requirements.txt

# Stage 2: Runtime
FROM python:3.11-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    libpq5 \
    libmagic1 \
    ffmpeg \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

RUN useradd -m -u 1000 -s /bin/bash worker

WORKDIR /app

# Only copy what was explicitly installed, not the builder's full bin/
COPY --from=builder /install /usr/local

COPY worker/ ./worker/

RUN mkdir -p /tmp/mpiper && chown -R worker:worker /tmp/mpiper /app

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    TEMP_DIR=/tmp/mpiper

USER worker

ENTRYPOINT ["python", "-m", "worker"]
