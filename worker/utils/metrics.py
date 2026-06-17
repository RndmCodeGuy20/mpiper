"""
worker.utils.metrics

OpenTelemetry metrics initialization and instrumentation for the Python worker.

This module provides metrics for:
- Queue message consumption (success/failure)
- Job processing (duration, success, failure)
- Asset processing (by type)
- Storage operations
- Database operations
"""

import socket
from typing import Optional

from opentelemetry import metrics
from opentelemetry.exporter.otlp.proto.grpc.metric_exporter import OTLPMetricExporter
from opentelemetry.sdk.metrics import MeterProvider
from opentelemetry.sdk.metrics.export import PeriodicExportingMetricReader
from opentelemetry.sdk.resources import Resource, SERVICE_NAME, SERVICE_VERSION, DEPLOYMENT_ENVIRONMENT, SERVICE_INSTANCE_ID

from worker.utils.logger import get_logger

logger = get_logger(__name__)

# Global meter for the worker
_meter: Optional[metrics.Meter] = None

# Metric instruments
queue_message_consumed: Optional[metrics.Counter] = None
queue_message_failed: Optional[metrics.Counter] = None
queue_processing_duration: Optional[metrics.Histogram] = None

job_processing_total: Optional[metrics.Counter] = None
job_processing_success: Optional[metrics.Counter] = None
job_processing_failed: Optional[metrics.Counter] = None
job_processing_duration: Optional[metrics.Histogram] = None

asset_processing_total: Optional[metrics.Counter] = None
asset_processing_success: Optional[metrics.Counter] = None
asset_processing_failed: Optional[metrics.Counter] = None
asset_processing_duration: Optional[metrics.Histogram] = None
asset_size_bytes: Optional[metrics.Histogram] = None

storage_operation_total: Optional[metrics.Counter] = None
storage_operation_errors: Optional[metrics.Counter] = None
storage_operation_duration: Optional[metrics.Histogram] = None

db_query_total: Optional[metrics.Counter] = None
db_query_errors: Optional[metrics.Counter] = None
db_query_duration: Optional[metrics.Histogram] = None


def init_metrics(
    service_name: str = "mpiper-worker",
    service_version: str = "dev",
    endpoint: str = "otel-collector:4317",
    deployment_env: str = "development",
    instance_id: Optional[str] = None,
    tls_insecure: bool = True,
) -> None:
    """Initialize OpenTelemetry metrics with OTLP exporter.

    All parameters should be sourced from the centralised config (get_config().otel)
    rather than read directly from environment variables.
    """
    global _meter
    global queue_message_consumed, queue_message_failed, queue_processing_duration
    global job_processing_total, job_processing_success, job_processing_failed, job_processing_duration
    global asset_processing_total, asset_processing_success, asset_processing_failed
    global asset_processing_duration, asset_size_bytes
    global storage_operation_total, storage_operation_errors, storage_operation_duration
    global db_query_total, db_query_errors, db_query_duration

    if _meter is not None:
        logger.warning("Metrics already initialized")
        return

    if "://" in endpoint:
        endpoint = endpoint.split("://", 1)[1]

    logger.info(f"Initializing OpenTelemetry metrics with endpoint: {endpoint}")

    resource = Resource.create({
        SERVICE_NAME: service_name,
        SERVICE_VERSION: service_version,
        DEPLOYMENT_ENVIRONMENT: deployment_env,
        SERVICE_INSTANCE_ID: instance_id or socket.gethostname(),
    })

    exporter = OTLPMetricExporter(
        endpoint=endpoint,
        insecure=tls_insecure,
    )

    # Create metric reader with 15-second export interval
    reader = PeriodicExportingMetricReader(exporter, export_interval_millis=15000)

    # Create meter provider
    provider = MeterProvider(resource=resource, metric_readers=[reader])
    metrics.set_meter_provider(provider)

    # Get meter
    _meter = metrics.get_meter(__name__)

    # Initialize Queue Metrics
    queue_message_consumed = _meter.create_counter(
        name="mpiper.queue.message.consumed",
        description="Total number of queue messages consumed",
        unit="{message}",
    )

    queue_message_failed = _meter.create_counter(
        name="mpiper.queue.message.failed",
        description="Total number of queue messages that failed processing",
        unit="{message}",
    )

    queue_processing_duration = _meter.create_histogram(
        name="mpiper.queue.processing.duration",
        description="Duration of queue message processing",
        unit="s",
    )

    # Initialize Job Metrics
    job_processing_total = _meter.create_counter(
        name="mpiper.job.processing.total",
        description="Total number of jobs processed",
        unit="{job}",
    )

    job_processing_success = _meter.create_counter(
        name="mpiper.job.processing.success",
        description="Total number of successfully processed jobs",
        unit="{job}",
    )

    job_processing_failed = _meter.create_counter(
        name="mpiper.job.processing.failed",
        description="Total number of failed job processing attempts",
        unit="{job}",
    )

    job_processing_duration = _meter.create_histogram(
        name="mpiper.job.processing.duration",
        description="Duration of job processing",
        unit="s",
    )

    # Initialize Asset Metrics
    asset_processing_total = _meter.create_counter(
        name="mpiper.asset.processing.total",
        description="Total number of assets processed",
        unit="{asset}",
    )

    asset_processing_success = _meter.create_counter(
        name="mpiper.asset.processing.success",
        description="Total number of successfully processed assets",
        unit="{asset}",
    )

    asset_processing_failed = _meter.create_counter(
        name="mpiper.asset.processing.failed",
        description="Total number of failed asset processing attempts",
        unit="{asset}",
    )

    asset_processing_duration = _meter.create_histogram(
        name="mpiper.asset.processing.duration",
        description="Duration of asset processing",
        unit="s",
    )

    asset_size_bytes = _meter.create_histogram(
        name="mpiper.asset.size.bytes",
        description="Size of processed assets in bytes",
        unit="By",
    )

    # Initialize Storage Metrics
    storage_operation_total = _meter.create_counter(
        name="mpiper.storage.operation.total",
        description="Total number of storage operations",
        unit="{operation}",
    )

    storage_operation_errors = _meter.create_counter(
        name="mpiper.storage.operation.errors",
        description="Total number of storage operation errors",
        unit="{error}",
    )

    storage_operation_duration = _meter.create_histogram(
        name="mpiper.storage.operation.duration",
        description="Duration of storage operations",
        unit="s",
    )

    # Initialize Database Metrics
    db_query_total = _meter.create_counter(
        name="mpiper.db.query.total",
        description="Total number of database queries",
        unit="{query}",
    )

    db_query_errors = _meter.create_counter(
        name="mpiper.db.query.errors",
        description="Total number of database query errors",
        unit="{error}",
    )

    db_query_duration = _meter.create_histogram(
        name="mpiper.db.query.duration",
        description="Duration of database queries",
        unit="s",
    )

    logger.info("OpenTelemetry metrics initialized successfully")


def get_meter() -> Optional[metrics.Meter]:
    """Get the global meter instance.
    
    Returns
    -------
    Optional[metrics.Meter]
        The meter instance, or None if not initialized
    """
    return _meter


def shutdown_metrics() -> None:
    """Shutdown the metrics provider and flush all pending metrics."""
    provider = metrics.get_meter_provider()
    if hasattr(provider, "shutdown"):
        provider.shutdown()
        logger.info("Metrics provider shutdown complete")
