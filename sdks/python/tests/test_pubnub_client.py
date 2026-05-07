"""
Tests for blocks_network.pubnub_client -- PubNub client factory.

Tests cover key validation, environment fallback, and configuration.
"""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest


class TestCreatePubnubClientValidation:
    def test_raises_value_error_when_subscribe_key_missing(self, monkeypatch) -> None:
        """No subscribe_key argument raises ValueError."""
        import blocks_network.pubnub_client as pc

        monkeypatch.setattr(pc, "_PUBNUB_AVAILABLE", True)

        with pytest.raises(ValueError, match="subscribe_key is required"):
            pc.create_pubnub_client()

    def test_raises_import_error_when_pubnub_unavailable(self, monkeypatch) -> None:
        """When _PUBNUB_AVAILABLE is False, raises ImportError."""
        import blocks_network.pubnub_client as pc

        monkeypatch.setattr(pc, "_PUBNUB_AVAILABLE", False)

        with pytest.raises(ImportError, match="pubnub"):
            pc.create_pubnub_client(subscribe_key="sub-c-test")


class TestCreatePubnubClientConfig:
    def test_creates_client_with_explicit_keys(self, monkeypatch) -> None:
        """Explicit keys are passed to PNConfiguration."""
        import blocks_network.pubnub_client as pc

        monkeypatch.setattr(pc, "_PUBNUB_AVAILABLE", True)

        mock_config_instance = MagicMock()
        mock_config_cls = MagicMock(return_value=mock_config_instance)
        mock_pubnub_cls = MagicMock()

        monkeypatch.setattr(pc, "PNConfiguration", mock_config_cls)
        monkeypatch.setattr(pc, "PubNub", mock_pubnub_cls)

        pc.create_pubnub_client(
            publish_key="pub-c-test",
            subscribe_key="sub-c-test",
            user_id="my-user",
        )

        assert mock_config_instance.subscribe_key == "sub-c-test"
        assert mock_config_instance.publish_key == "pub-c-test"
        assert mock_config_instance.user_id == "my-user"
        mock_pubnub_cls.assert_called_once_with(mock_config_instance)

    def test_does_not_accept_secret_key(self, monkeypatch) -> None:
        """create_pubnub_client must not accept a secret_key parameter."""
        import blocks_network.pubnub_client as pc
        import inspect

        sig = inspect.signature(pc.create_pubnub_client)
        assert "secret_key" not in sig.parameters

    def test_no_secret_key_on_config(self, monkeypatch) -> None:
        """PNConfiguration must not have secret_key set."""
        import blocks_network.pubnub_client as pc

        monkeypatch.setattr(pc, "_PUBNUB_AVAILABLE", True)

        mock_config_instance = MagicMock()
        mock_config_cls = MagicMock(return_value=mock_config_instance)
        mock_pubnub_cls = MagicMock()

        monkeypatch.setattr(pc, "PNConfiguration", mock_config_cls)
        monkeypatch.setattr(pc, "PubNub", mock_pubnub_cls)

        # Set secret_key in env -- should be ignored
        monkeypatch.setenv("PUBNUB_SECRET_KEY", "sec-leaked")

        pc.create_pubnub_client(subscribe_key="sub-c-test")

        # Verify secret_key was never assigned
        assert not hasattr(mock_config_instance, "secret_key") or not any(
            c for c in dir(mock_config_instance)
            if c == "secret_key" and getattr(mock_config_instance, c) == "sec-leaked"
        )

    def test_default_user_id_fallback(self, monkeypatch) -> None:
        """When no user_id provided, falls back to 'blocks-agent'."""
        import blocks_network.pubnub_client as pc

        monkeypatch.setattr(pc, "_PUBNUB_AVAILABLE", True)

        mock_config_instance = MagicMock()
        mock_config_cls = MagicMock(return_value=mock_config_instance)
        mock_pubnub_cls = MagicMock()

        monkeypatch.setattr(pc, "PNConfiguration", mock_config_cls)
        monkeypatch.setattr(pc, "PubNub", mock_pubnub_cls)

        pc.create_pubnub_client(subscribe_key="sub-c-test")

        assert mock_config_instance.user_id == "blocks-agent"

    def test_explicit_user_id_overrides_default(self, monkeypatch) -> None:
        """Explicit user_id param takes precedence over the default."""
        import blocks_network.pubnub_client as pc

        monkeypatch.setattr(pc, "_PUBNUB_AVAILABLE", True)

        mock_config_instance = MagicMock()
        mock_config_cls = MagicMock(return_value=mock_config_instance)
        mock_pubnub_cls = MagicMock()

        monkeypatch.setattr(pc, "PNConfiguration", mock_config_cls)
        monkeypatch.setattr(pc, "PubNub", mock_pubnub_cls)

        pc.create_pubnub_client(subscribe_key="sub-c-test", user_id="AG-my-agent-1")

        assert mock_config_instance.user_id == "AG-my-agent-1"

    def test_uses_explicit_keys(self, monkeypatch) -> None:
        """Explicit keys take precedence."""
        import blocks_network.pubnub_client as pc

        monkeypatch.setattr(pc, "_PUBNUB_AVAILABLE", True)

        mock_config_instance = MagicMock()
        mock_config_cls = MagicMock(return_value=mock_config_instance)
        mock_pubnub_cls = MagicMock()

        monkeypatch.setattr(pc, "PNConfiguration", mock_config_cls)
        monkeypatch.setattr(pc, "PubNub", mock_pubnub_cls)

        pc.create_pubnub_client(
            subscribe_key="sub-c-explicit",
            publish_key="pub-c-explicit",
        )

        assert mock_config_instance.subscribe_key == "sub-c-explicit"
        assert mock_config_instance.publish_key == "pub-c-explicit"
        assert mock_config_instance.user_id == "blocks-agent"
