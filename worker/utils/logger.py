import logging
import os
from typing import Optional


_DEFAULT_FORMAT = "%(asctime)s %(levelname)s [%(name)s] %(message)s"


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
