from __future__ import annotations

import logging
from typing import Callable, TypeVar

logger = logging.getLogger(__name__)

T = TypeVar("T")


def noop(_: object) -> None:
    pass


def invoke(callback: Callable[[T], None], value: T) -> None:
    """Run an application callback; an exception it raises must not break the pipeline."""
    try:
        callback(value)
    except Exception:
        logger.exception("session callback raised")
