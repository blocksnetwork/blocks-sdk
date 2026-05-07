"""
CDM (Config Distribution Manager) fetcher.

Fetches PubNub key configuration from a remote JSON endpoint.
Provides two keysets (playground + network) for dual-instance operation.
"""

from __future__ import annotations

import json
import os
import urllib.request
from dataclasses import dataclass
from typing import Optional

DEFAULT_CDM_URL = "https://config.blocks.ai/config.json"


@dataclass(frozen=True)
class CdmKeyset:
    """PubNub keyset from CDM config."""

    publish_key: str
    subscribe_key: str


@dataclass(frozen=True)
class CdmApiConfig:
    """API configuration from CDM config."""

    base_url: str
    client_id: Optional[str] = None


@dataclass(frozen=True)
class CdmConfig:
    """Full CDM configuration with dual keysets."""

    playground: CdmKeyset
    network: CdmKeyset
    api: CdmApiConfig


PnEnvironment = str  # Literal["playground", "network"]


def fetch_cdm_config(url: Optional[str] = None) -> CdmConfig:
    """Fetch CDM config from an HTTP(S) URL.

    URL resolution: explicit ``url`` > ``BLOCKS_CDM_URL`` env var > hardcoded default.

    Raises
    ------
    ValueError
        If required keys are missing from the response.
    urllib.error.HTTPError
        If the HTTP request fails.
    """
    cdm_url = url or os.environ.get("BLOCKS_CDM_URL", "") or DEFAULT_CDM_URL
    data = _load_cdm_data(cdm_url)

    pg = data.get("playground") or {}
    nw = data.get("network") or {}

    if not pg.get("publishKey") or not pg.get("subscribeKey"):
        raise ValueError("CDM config missing playground keys")
    if not nw.get("publishKey") or not nw.get("subscribeKey"):
        raise ValueError("CDM config missing network keys")

    api_raw = data.get("api") or {}

    return CdmConfig(
        playground=CdmKeyset(
            publish_key=pg["publishKey"],
            subscribe_key=pg["subscribeKey"],
        ),
        network=CdmKeyset(
            publish_key=nw["publishKey"],
            subscribe_key=nw["subscribeKey"],
        ),
        api=CdmApiConfig(
            base_url=api_raw.get("baseUrl", ""),
            client_id=api_raw.get("clientId"),
        ),
    )


def _load_cdm_data(cdm_url: str) -> dict:
    req = urllib.request.Request(cdm_url)
    with urllib.request.urlopen(req, timeout=10) as resp:
        return json.loads(resp.read().decode("utf-8"))
