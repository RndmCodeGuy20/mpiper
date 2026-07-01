"""
worker.utils.tracing

OpenTelemetry tracing initialization for the Python worker.

Mirrors `worker.utils.metrics`: an OTLP gRPC exporter to the same collector
endpoint, a BatchSpanProcessor, and the SAME W3C propagators as the Go API
(`traceparent` + `baggage`) so the trace continues across the Redis boundary
instead of starting fresh.

The worker had OTel metric instruments but no tracer and no context extraction,
so the distributed trace died at the queue. This closes that gap on the consumer
side; `worker.consumer.consumer` extracts the producer context and starts the
consume span as a child (with a link) of it.
"""

from typing import Optional

from opentelemetry import trace
from opentelemetry.baggage.propagation import W3CBaggagePropagator
from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
from opentelemetry.propagate import set_global_textmap
from opentelemetry.propagators.composite import CompositePropagator
from opentelemetry.sdk.resources import (
    DEPLOYMENT_ENVIRONMENT,
    SERVICE_INSTANCE_ID,
    SERVICE_NAME,
    SERVICE_VERSION,
    Resource,
)
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.sdk.trace.sampling import ALWAYS_ON, ParentBased, TraceIdRatioBased
from opentelemetry.trace.propagation.tracecontext import TraceContextTextMapPropagator

from worker.utils.logger import get_logger

logger = get_logger(__name__)

# Global tracer for the worker
_tracer: Optional[trace.Tracer] = None
_provider: Optional[TracerProvider] = None


def _build_sampler(deployment_env: str, sampling_rate: float):
    """AlwaysSample in dev/local; parent-based ratio sampling otherwise.

    Matches the Go API's getSampler() so both services agree on what to keep.
    """
    if deployment_env in ("development", "dev", "local", ""):
        return ALWAYS_ON
    return ParentBased(root=TraceIdRatioBased(sampling_rate))


def init_tracing(
    service_name: str = "mpiper-worker",
    service_version: str = "dev",
    endpoint: str = "otel-collector:4317",
    deployment_env: str = "development",
    instance_id: Optional[str] = None,
    tls_insecure: bool = True,
    sampling_rate: float = 1.0,
) -> None:
    """Initialize OpenTelemetry tracing with an OTLP gRPC span exporter.

    Parameters should be sourced from the centralised config (get_config().otel).
    Idempotent: a second call is a no-op so the worker can call it safely on
    startup alongside init_metrics.
    """
    global _tracer, _provider

    if _tracer is not None:
        logger.warning("Tracing already initialized")
        return

    if "://" in endpoint:
        endpoint = endpoint.split("://", 1)[1]

    logger.info(f"Initializing OpenTelemetry tracer with endpoint: {endpoint}")

    resource = Resource.create(
        {
            SERVICE_NAME: service_name,
            SERVICE_VERSION: service_version,
            DEPLOYMENT_ENVIRONMENT: deployment_env,
            SERVICE_INSTANCE_ID: instance_id or service_name,
        }
    )

    exporter = OTLPSpanExporter(endpoint=endpoint, insecure=tls_insecure)

    provider = TracerProvider(
        resource=resource,
        sampler=_build_sampler(deployment_env, sampling_rate),
    )
    provider.add_span_processor(BatchSpanProcessor(exporter))
    trace.set_tracer_provider(provider)

    # Same propagators as the Go API (composite TraceContext + Baggage) so the
    # traceparent the producer injected is understood here.
    set_global_textmap(
        CompositePropagator(
            [TraceContextTextMapPropagator(), W3CBaggagePropagator()]
        )
    )

    _provider = provider
    _tracer = trace.get_tracer(__name__)
    logger.info("OpenTelemetry tracer initialized successfully")


def get_tracer() -> Optional[trace.Tracer]:
    """Return the global worker tracer, or None if init_tracing was not called."""
    return _tracer


def shutdown_tracing() -> None:
    """Flush and shut down the tracer provider on exit."""
    if _provider is not None:
        _provider.shutdown()
        logger.info("Tracer provider shutdown complete")
