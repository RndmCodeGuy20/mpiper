import logging
import os
from typing import Optional

from opentelemetry import trace


# trace_id=<hex> matches the Grafana Loki derived-field regex (trace_id=(\w+)),
# which links each log line to its Tempo trace. span_id is included for context.
_DEFAULT_FORMAT = (
    "%(asctime)s %(levelname)s [%(name)s] "
    "trace_id=%(trace_id)s span_id=%(span_id)s %(message)s"
)


class TraceContextFilter(logging.Filter):
    """Inject the active span's trace_id/span_id into every log record.

    Emits empty strings when there is no active recording span, so the Grafana
    derived field does not create a link to a non-existent trace.
    """

    def filter(self, record: logging.LogRecord) -> bool:
        ctx = trace.get_current_span().get_span_context()
        if ctx is not None and ctx.is_valid:
            record.trace_id = format(ctx.trace_id, "032x")
            record.span_id = format(ctx.span_id, "016x")
        else:
            record.trace_id = ""
            record.span_id = ""
        return True


def setup_logging(
    level: Optional[str] = None,
    fmt: str = _DEFAULT_FORMAT,
) -> None:
    """
    Set up logging configuration.
    Args:
        level (Optional[str]): Logging level as a string (e.g., 'DEBUG', 'INFO').
                               If None, it reads from the LOG_LEVEL environment variable or defaults to 'INFO'.
        fmt (str): Format string for log messages.

    Returns:
        None
    """
    if level is None:
        level = os.getenv("LOG_LEVEL", "INFO")

    log_level = getattr(logging, level.upper(), logging.INFO)

    # Prevent duplicate handlers if setup_logging is called twice
    root = logging.getLogger()
    if root.handlers:
        return

    logging.basicConfig(
        level=log_level,
        format=fmt,
    )

    # Attach the trace-context filter at the handler level so it stamps every
    # record flowing through, regardless of which logger emitted it.
    trace_filter = TraceContextFilter()
    for handler in logging.getLogger().handlers:
        handler.addFilter(trace_filter)

    # Silence noisy libraries (optional, but recommended)
    logging.getLogger("urllib3").setLevel(logging.WARNING)
    logging.getLogger("botocore").setLevel(logging.WARNING)
    logging.getLogger("google").setLevel(logging.WARNING)


def get_logger(name: str) -> logging.Logger:
    """
    Get a logger by name.
    Args:
        name (str): Name of the logger.
    Returns:
        logging.Logger: Configured logger instance.
    """
    return logging.getLogger(name)
