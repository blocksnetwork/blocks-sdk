"""Tests for CDM config fetcher."""

import json
from unittest.mock import MagicMock, patch

import pytest


MOCK_CDM_RESPONSE = {
    "playground": {
        "publishKey": "pub-c-playground-key",
        "subscribeKey": "sub-c-playground-key",
    },
    "network": {
        "publishKey": "pub-c-network-key",
        "subscribeKey": "sub-c-network-key",
    },
    "api": {
        "baseUrl": "http://localhost:3001",
    },
}


class TestFetchCdmConfig:
    """Tests for fetch_cdm_config()."""

    def _mock_urlopen(self, data: dict) -> MagicMock:
        mock_resp = MagicMock()
        mock_resp.read.return_value = json.dumps(data).encode("utf-8")
        mock_resp.__enter__ = lambda s: s
        mock_resp.__exit__ = MagicMock(return_value=False)
        return mock_resp

    @patch("blocks_network.cdm_config.urllib.request.urlopen")
    def test_fetches_from_default_url(self, mock_urlopen: MagicMock) -> None:
        from blocks_network.cdm_config import fetch_cdm_config, DEFAULT_CDM_URL

        mock_urlopen.return_value = self._mock_urlopen(MOCK_CDM_RESPONSE)

        config = fetch_cdm_config()

        call_args = mock_urlopen.call_args
        req = call_args[0][0]
        assert req.full_url == DEFAULT_CDM_URL
        assert config.playground.publish_key == "pub-c-playground-key"
        assert config.network.subscribe_key == "sub-c-network-key"
        assert config.api.base_url == "http://localhost:3001"

    @patch("blocks_network.cdm_config.urllib.request.urlopen")
    def test_uses_env_var(self, mock_urlopen: MagicMock, monkeypatch: pytest.MonkeyPatch) -> None:
        from blocks_network.cdm_config import fetch_cdm_config

        monkeypatch.setenv("BLOCKS_CDM_URL", "https://custom.example.com/config.json")
        mock_urlopen.return_value = self._mock_urlopen(MOCK_CDM_RESPONSE)

        fetch_cdm_config()

        req = mock_urlopen.call_args[0][0]
        assert req.full_url == "https://custom.example.com/config.json"

    @patch("blocks_network.cdm_config.urllib.request.urlopen")
    def test_explicit_url_overrides_env(self, mock_urlopen: MagicMock, monkeypatch: pytest.MonkeyPatch) -> None:
        from blocks_network.cdm_config import fetch_cdm_config

        monkeypatch.setenv("BLOCKS_CDM_URL", "https://env.example.com/config.json")
        mock_urlopen.return_value = self._mock_urlopen(MOCK_CDM_RESPONSE)

        fetch_cdm_config("https://explicit.example.com/config.json")

        req = mock_urlopen.call_args[0][0]
        assert req.full_url == "https://explicit.example.com/config.json"

    @patch("blocks_network.cdm_config.urllib.request.urlopen")
    def test_raises_on_missing_playground(self, mock_urlopen: MagicMock) -> None:
        from blocks_network.cdm_config import fetch_cdm_config

        bad = {k: v for k, v in MOCK_CDM_RESPONSE.items() if k != "playground"}
        mock_urlopen.return_value = self._mock_urlopen(bad)

        with pytest.raises(ValueError, match="missing playground keys"):
            fetch_cdm_config()

    @patch("blocks_network.cdm_config.urllib.request.urlopen")
    def test_raises_on_missing_network(self, mock_urlopen: MagicMock) -> None:
        from blocks_network.cdm_config import fetch_cdm_config

        bad = {k: v for k, v in MOCK_CDM_RESPONSE.items() if k != "network"}
        mock_urlopen.return_value = self._mock_urlopen(bad)

        with pytest.raises(ValueError, match="missing network keys"):
            fetch_cdm_config()

    @patch("blocks_network.cdm_config.urllib.request.urlopen")
    def test_raises_on_http_error(self, mock_urlopen: MagicMock) -> None:
        from blocks_network.cdm_config import fetch_cdm_config
        import urllib.error

        mock_urlopen.side_effect = urllib.error.HTTPError(
            url="http://x", code=404, msg="Not Found", hdrs=None, fp=None,  # type: ignore[arg-type]
        )

        with pytest.raises(urllib.error.HTTPError):
            fetch_cdm_config()
