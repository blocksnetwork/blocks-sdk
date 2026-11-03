"""Where a consumer client finds its PubNub keys and backend: explicit option, then env, then CDM."""

from __future__ import annotations

import os
from dataclasses import dataclass
from typing import Optional

from . import cdm_config


@dataclass(frozen=True)
class ConsumerEndpoints:
    subscribe_key: str
    publish_key: Optional[str]
    base_url: str


def resolve_consumer_endpoints(
    billing_mode: str,
    *,
    cdm_url: Optional[str] = None,
    subscribe_key: Optional[str] = None,
    publish_key: Optional[str] = None,
    base_url: Optional[str] = None,
) -> ConsumerEndpoints:
    """Resolve each value as explicit option, then ``BLOCKS_*`` env, then CDM config.

    ``'free'`` selects the playground keyset and ``'paid'`` the network keyset.
    """
    if billing_mode not in ("free", "paid"):
        raise ValueError(f"billing_mode must be 'free' or 'paid', got {billing_mode!r}")
    cdm = cdm_config.fetch_cdm_config(cdm_url or os.environ.get("BLOCKS_CDM_URL") or None)
    keyset = cdm.playground if billing_mode == "free" else cdm.network
    resolved_subscribe_key = (
        subscribe_key or os.environ.get("BLOCKS_SUBSCRIBE_KEY") or keyset.subscribe_key
    )
    resolved_publish_key = publish_key or os.environ.get("BLOCKS_PUBLISH_KEY") or keyset.publish_key
    resolved_base_url = base_url or os.environ.get("BLOCKS_BACKEND_URL") or cdm.api.base_url
    if not resolved_subscribe_key:
        raise ValueError(
            "subscribe_key could not be resolved from explicit option, "
            "BLOCKS_SUBSCRIBE_KEY env var, or CDM config"
        )
    if not resolved_base_url:
        raise ValueError(
            "base_url could not be resolved from explicit option, "
            "BLOCKS_BACKEND_URL env var, or CDM config. "
            "base_url is required for RPC calls."
        )
    return ConsumerEndpoints(resolved_subscribe_key, resolved_publish_key, resolved_base_url)
