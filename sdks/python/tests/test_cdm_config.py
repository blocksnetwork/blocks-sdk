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


@pytest.fixture
def https_cdm_server(tmp_path):
    """Serve MOCK_CDM_RESPONSE over TLS with a throwaway self-signed CA.

    Yields ``(url, ca_file)``. ``ca_file`` is in no system or certifi store,
    so only a client that verifies against exactly that file can connect.
    """
    import http.server
    import shutil
    import ssl
    import subprocess
    import threading

    if shutil.which("openssl") is None:
        pytest.skip("openssl CLI not available to mint a test certificate")
    cert, key = tmp_path / "cert.pem", tmp_path / "key.pem"
    subprocess.run(
        [
            "openssl", "req", "-x509", "-newkey", "ec",
            "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes",
            "-keyout", str(key), "-out", str(cert), "-days", "1",
            "-subj", "/CN=127.0.0.1", "-addext", "subjectAltName=IP:127.0.0.1",
        ],
        check=True,
        capture_output=True,
    )

    body = json.dumps(MOCK_CDM_RESPONSE).encode("utf-8")

    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self) -> None:
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args: object) -> None:
            pass

    server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain(cert, key)
    server.socket = ctx.wrap_socket(server.socket, server_side=True)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield f"https://127.0.0.1:{server.server_port}/config.json", str(cert)
    finally:
        server.shutdown()
        server.server_close()


class TestCdmConfigTrust:
    """The CDM response sets the backend URL and PubNub keys, so it must be
    verified against the SDK's certifi bundle like every other SDK request,
    not against the interpreter's default store."""

    def test_trusts_the_sdk_bundle(self, https_cdm_server, monkeypatch) -> None:
        import certifi

        from blocks_network.cdm_config import fetch_cdm_config

        url, ca_file = https_cdm_server
        monkeypatch.setattr(certifi, "where", lambda: ca_file)

        config = fetch_cdm_config(url)

        assert config.playground.publish_key == "pub-c-playground-key"

    def test_rejects_a_server_outside_the_sdk_bundle(self, https_cdm_server) -> None:
        import ssl
        import urllib.error

        from blocks_network.cdm_config import fetch_cdm_config

        url, _ = https_cdm_server

        with pytest.raises(urllib.error.URLError) as excinfo:
            fetch_cdm_config(url)
        assert isinstance(excinfo.value.reason, ssl.SSLCertVerificationError)

    def test_refuses_a_non_http_url(self, tmp_path) -> None:
        from blocks_network.cdm_config import fetch_cdm_config

        local = tmp_path / "config.json"
        local.write_text(json.dumps(MOCK_CDM_RESPONSE))

        with pytest.raises(ValueError, match="http or https"):
            fetch_cdm_config(local.as_uri())
