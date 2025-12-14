# Stage 1: Builder - Install dependencies
FROM python:3.11-slim AS builder

# Install system dependencies required for Python packages
RUN apt-get update && apt-get install -y \
    gcc \
    g++ \
    libpq-dev \
    libmagic1 \
    && rm -rf /var/lib/apt/lists/*

# Install Poetry
RUN pip install --no-cache-dir poetry==1.7.1

WORKDIR /build

# Copy dependency files
COPY pyproject.toml poetry.lock* poetry.toml ./

# Configure Poetry to not create virtual environments (we're in a container)
RUN poetry config virtualenvs.create false

# Install dependencies
RUN poetry install --no-dev --no-interaction --no-ansi

# Stage 2: Runtime
FROM python:3.11-slim

# Install runtime dependencies + ffmpeg
RUN apt-get update && apt-get install -y \
    libpq5 \
    libmagic1 \
    ffmpeg \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Optional: sanity check
RUN ffmpeg -version && ffprobe -version

# Create non-root user
RUN useradd -m -u 1000 -s /bin/bash worker

WORKDIR /app

# Copy Python dependencies from builder
COPY --from=builder /usr/local/lib/python3.11/site-packages /usr/local/lib/python3.11/site-packages
COPY --from=builder /usr/local/bin /usr/local/bin

# Copy application code
COPY worker/ ./worker/

# Create temp directory for processing
RUN mkdir -p /tmp/mpiper && chown -R worker:worker /tmp/mpiper /app

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    TEMP_DIR=/tmp/mpiper

USER worker

ENTRYPOINT ["python", "-m", "worker"]
