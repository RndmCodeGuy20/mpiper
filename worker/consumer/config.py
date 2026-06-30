import os
import socket
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from typing import Optional


@dataclass
class DatabaseConfig:
    host: str
    port: int
    user: str
    password: str
    db_name: str
    ssl_mode: bool
    ssl_cert_path: Optional[str] = None
    pool_size: int = 10
    pool_timeout: int = 30
    max_retries: int = 5
    retry_delay: int = 5

    @staticmethod
    def from_env() -> "DatabaseConfig":
        return DatabaseConfig(
            host=os.getenv("DB_HOST", "localhost"),
            port=int(os.getenv("DB_PORT", "5432")),
            user=os.getenv("DB_USER", "user"),
            password=os.getenv("DB_PASSWORD", "password"),
            db_name=os.getenv("DB_NAME", "media_db"),
            ssl_mode=os.getenv("DB_SSL_MODE", "false").lower() == "true",
            ssl_cert_path=os.getenv("DB_SSL_CERT_PATH"),
            pool_size=int(os.getenv("DB_POOL_SIZE", "10")),
            pool_timeout=int(os.getenv("DB_POOL_TIMEOUT", "30")),
            max_retries=int(os.getenv("DB_MAX_RETRIES", "5")),
            retry_delay=int(os.getenv("DB_RETRY_DELAY", "5")),
        )


@dataclass
class RedisConfig:
    connection_string: str
    pool_size: int = 10
    pool_timeout: int = 30
    max_retries: int = 5
    retry_delay: int = 5
    connect_timeout: int = 10
    read_timeout: int = 10
    write_timeout: int = 10

    @staticmethod
    def from_env() -> "RedisConfig":
        return RedisConfig(
            connection_string=os.getenv(
                "REDIS_CONNECTION_STRING", "redis://localhost:6379/0"
            ),
            pool_size=int(os.getenv("REDIS_POOL_SIZE", "10")),
            pool_timeout=int(os.getenv("REDIS_POOL_TIMEOUT", "30")),
            max_retries=int(os.getenv("REDIS_MAX_RETRIES", "5")),
            retry_delay=int(os.getenv("REDIS_RETRY_DELAY", "5")),
            connect_timeout=int(os.getenv("REDIS_CONNECT_TIMEOUT", "10")),
            read_timeout=int(os.getenv("REDIS_READ_TIMEOUT", "10")),
            write_timeout=int(os.getenv("REDIS_WRITE_TIMEOUT", "10")),
        )


@dataclass
class BucketConfig:
    bucket_name: str
    region: str
    access_key: str
    secret_key: str
    endpoint_url: Optional[str] = None
    public_endpoint_url: Optional[str] = None
    provider: str = "gcs"
    sa_path: Optional[str] = None

    @staticmethod
    def from_env() -> "BucketConfig":
        # S3_* names mirror the Go server (internal/config/env.go); they take
        # precedence over the generic BUCKET_* names so a single .env drives
        # both services. BUCKET_* remains the fallback / GCS default.
        bucket_name = os.getenv("S3_BUCKET_NAME") or os.getenv("BUCKET_NAME", "media-bucket")
        endpoint_url = os.getenv("S3_ENDPOINT_URL") or os.getenv("BUCKET_ENDPOINT_URL")
        return BucketConfig(
            provider=os.getenv("BUCKET_PROVIDER", "gcs"),
            bucket_name=bucket_name,
            region=os.getenv("S3_REGION") or os.getenv("BUCKET_REGION", "us-east-1"),
            access_key=os.getenv("S3_ACCESS_KEY_ID") or os.getenv("BUCKET_ACCESS_KEY", ""),
            secret_key=os.getenv("S3_SECRET_ACCESS_KEY") or os.getenv("BUCKET_SECRET_KEY", ""),
            endpoint_url=endpoint_url,
            # Client-facing endpoint baked into persisted variant URLs. Object
            # I/O still uses endpoint_url (internal); falls back to it when unset.
            public_endpoint_url=os.getenv("S3_PUBLIC_ENDPOINT_URL") or endpoint_url,
            sa_path=os.getenv("GCS_SA_PATH") or os.getenv("BUCKET_SA_PATH"),
        )


@dataclass
class OtelConfig:
    endpoint: str
    service_name: str
    service_version: str
    deployment_env: str
    tls_insecure: bool = True
    instance_id: str = field(default_factory=socket.gethostname)

    @staticmethod
    def from_env(service_name: str = "mpiper-worker", service_version: str = "dev") -> "OtelConfig":
        return OtelConfig(
            endpoint=os.getenv("OTEL_EXPORTER_OTLP_ENDPOINT", "otel-collector:4317"),
            service_name=os.getenv("SERVICE_NAME", service_name),
            service_version=os.getenv("SERVICE_VERSION", service_version),
            deployment_env=os.getenv("DEPLOYMENT_ENV", "development"),
            tls_insecure=os.getenv("OTEL_TLS_INSECURE", "true").lower() == "true",
            instance_id=os.getenv("HOSTNAME", socket.gethostname()),
        )


@dataclass
class WorkerConfig:
    database: DatabaseConfig
    redis: RedisConfig
    bucket: BucketConfig
    otel: OtelConfig
    temp_dir: str
    stream_name: str
    worker_id: str
    log_level: str
    auto_migrate: bool
    migrations_dir: str
    consumer_group: str = "worker-group"
    max_concurrent_jobs: int = 5
    job_poll_interval: int = 10

    @staticmethod
    def from_env() -> "WorkerConfig":
        curr_os = os.name
        if curr_os == "nt":
            temp_dir = os.getenv("TEMP", "C:\\Temp\\worker")
        else:
            temp_dir = os.getenv("TMPDIR", "/tmp/worker")

        default_migrations_dir = str(
            Path(__file__).resolve().parents[2] / "internal" / "database" / "migrations"
        )

        return WorkerConfig(
            database=DatabaseConfig.from_env(),
            redis=RedisConfig.from_env(),
            bucket=BucketConfig.from_env(),
            otel=OtelConfig.from_env(),
            worker_id=(
                os.getenv("WORKER_ID")
                or os.getenv("HOSTNAME")
                or socket.gethostname()
                or str(uuid.uuid4())
            ),
            max_concurrent_jobs=int(os.getenv("MAX_CONCURRENT_JOBS", "5")),
            job_poll_interval=int(os.getenv("JOB_POLL_INTERVAL", "10")),
            temp_dir=temp_dir,
            stream_name=os.getenv("STREAM_NAME", "media:jobs"),
            consumer_group=os.getenv("CONSUMER_GROUP", "worker-group"),
            log_level=os.getenv("LOG_LEVEL", "INFO"),
            auto_migrate=os.getenv("AUTO_MIGRATE", "false").lower() == "true",
            migrations_dir=os.getenv("MIGRATIONS_DIR", default_migrations_dir),
        )


# --- Singleton ---

_instance: Optional[WorkerConfig] = None


def get_config() -> WorkerConfig:
    """Return the process-level WorkerConfig singleton, initialising on first call."""
    global _instance
    if _instance is None:
        _instance = WorkerConfig.from_env()
    return _instance


def init_config(cfg: WorkerConfig) -> None:
    """Explicitly set the singleton (useful in tests or when config is built externally)."""
    global _instance
    _instance = cfg
