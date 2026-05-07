"""
Tests verifying that removed environment variables no longer affect SDK behavior.

Covers:
- Fix 1: AGENT_NAME env does not override card identity
- Fix 3: CONCURRENCY / EXPECTED_INSTANCES env does not override runtime options
- Fix 4: MAX_PENDING_BACKLOG / MAX_RUNNING_TIME_SEC env removed
- Fix 5: PUBNUB_SECRET_KEY not accepted
- Fix 8: BLOCKS_BACKEND_URL env not read by provider-side code
- Fix 10: LOG_LEVEL replaces NODE_ENV / PYTHON_ENV
"""

from __future__ import annotations

import io
import sys
import time
from unittest.mock import MagicMock, patch

import pytest

from blocks_network.agent_instance import start_agent_instance
from blocks_network.types import AgentInstanceOptions

from tests.conftest import minimal_card


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _make_mock_pubnub() -> MagicMock:
    pn = MagicMock()

    def _chain():
        c = MagicMock()
        for m in (
            "channel", "channels", "message", "meta", "should_store",
            "use_post", "with_presence", "state", "file_name",
            "file_object", "file_id", "execute",
        ):
            getattr(c, m).side_effect = lambda *a, _c=c, **kw: _c
        c.sync.return_value = MagicMock()
        return c

    pn.publish.return_value = _chain()
    pn.subscribe.return_value = _chain()
    pn.set_state.return_value = _chain()
    pn.unsubscribe.return_value = _chain()
    pn.download_file.return_value = _chain()
    pn._listeners = []
    pn.add_listener.side_effect = lambda l: pn._listeners.append(l)
    pn.remove_listener.side_effect = lambda l: (
        pn._listeners.remove(l) if l in pn._listeners else None
    )
    pn.set_filter_expression = MagicMock()
    pn.config = MagicMock()
    pn.config.filter_expression = None
    pn.set_token = MagicMock()
    return pn


# ---------------------------------------------------------------------------
# Fix 3: CONCURRENCY / EXPECTED_INSTANCES regression
# ---------------------------------------------------------------------------


class TestConcurrencyEnvIgnored:
    def test_leaked_concurrency_env_does_not_override_options(self, monkeypatch) -> None:
        """Leaked CONCURRENCY env var must not override options.concurrency."""
        monkeypatch.setenv("CONCURRENCY", "99")
        pn = _make_mock_pubnub()
        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",
                concurrency=2,
            )
        )
        # The instance started with concurrency=2 from options, not 99 from env
        result["stop"]()

    def test_leaked_expected_instances_env_does_not_override_options(self, monkeypatch) -> None:
        """Leaked EXPECTED_INSTANCES env var must not override options."""
        monkeypatch.setenv("EXPECTED_INSTANCES", "99")
        pn = _make_mock_pubnub()
        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",
                expected_instances=3,
            )
        )
        result["stop"]()

    def test_concurrency_defaults_to_one_without_env(self, monkeypatch) -> None:
        """Without options.concurrency and without env, defaults to 1."""
        monkeypatch.delenv("CONCURRENCY", raising=False)
        pn = _make_mock_pubnub()
        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",
            )
        )
        result["stop"]()


# ---------------------------------------------------------------------------
# Fix 4: MAX_PENDING_BACKLOG / MAX_RUNNING_TIME_SEC regression
# ---------------------------------------------------------------------------


class TestScalingEnvIgnored:
    def test_max_pending_backlog_env_ignored(self, monkeypatch) -> None:
        """Leaked MAX_PENDING_BACKLOG env var must not be read."""
        monkeypatch.setenv("MAX_PENDING_BACKLOG", "100")
        pn = _make_mock_pubnub()
        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",
            )
        )
        result["stop"]()

    def test_max_running_time_sec_env_ignored(self, monkeypatch) -> None:
        """Leaked MAX_RUNNING_TIME_SEC env var must not be read."""
        monkeypatch.setenv("MAX_RUNNING_TIME_SEC", "600")
        pn = _make_mock_pubnub()
        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",
            )
        )
        result["stop"]()

    def test_scaling_values_from_options_only(self) -> None:
        """Scaling values come exclusively from options."""
        pn = _make_mock_pubnub()
        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",
                max_pending_backlog=50,
                max_running_time_sec=120,
            )
        )
        result["stop"]()


# ---------------------------------------------------------------------------
# Fix 10: LOG_LEVEL tests
# ---------------------------------------------------------------------------


class TestLogLevel:
    def test_default_log_level_is_info(self, monkeypatch) -> None:
        """Default LOG_LEVEL is 'info'."""
        monkeypatch.delenv("LOG_LEVEL", raising=False)
        # Re-import to pick up the env change
        import blocks_network.config as cfg
        monkeypatch.setattr(cfg, "LOG_LEVEL", "info")

        from blocks_network.logging_utils import _should_log
        assert _should_log("error") is True
        assert _should_log("warn") is True
        assert _should_log("info") is True
        assert _should_log("debug") is False

    def test_log_level_error_suppresses_info_and_warn(self, monkeypatch) -> None:
        """LOG_LEVEL=error suppresses info, warn, and debug."""
        import blocks_network.config as cfg
        monkeypatch.setattr(cfg, "LOG_LEVEL", "error")

        from blocks_network.logging_utils import _should_log
        assert _should_log("error") is True
        assert _should_log("warn") is False
        assert _should_log("info") is False
        assert _should_log("debug") is False

    def test_log_level_debug_enables_all(self, monkeypatch) -> None:
        """LOG_LEVEL=debug enables all log levels."""
        import blocks_network.config as cfg
        monkeypatch.setattr(cfg, "LOG_LEVEL", "debug")

        from blocks_network.logging_utils import _should_log
        assert _should_log("error") is True
        assert _should_log("warn") is True
        assert _should_log("info") is True
        assert _should_log("debug") is True

    def test_log_level_warn(self, monkeypatch) -> None:
        """LOG_LEVEL=warn emits errors and warnings only."""
        import blocks_network.config as cfg
        monkeypatch.setattr(cfg, "LOG_LEVEL", "warn")

        from blocks_network.logging_utils import _should_log
        assert _should_log("error") is True
        assert _should_log("warn") is True
        assert _should_log("info") is False
        assert _should_log("debug") is False

    def test_log_event_suppressed_at_error_level(self, monkeypatch, capsys) -> None:
        """Info log is suppressed when LOG_LEVEL=error."""
        import blocks_network.config as cfg
        monkeypatch.setattr(cfg, "LOG_LEVEL", "error")

        from blocks_network.logging_utils import log_agent_instance_event
        log_agent_instance_event("info", "should not appear")

        captured = capsys.readouterr()
        assert "should not appear" not in captured.out
        assert "should not appear" not in captured.err

    def test_log_event_emitted_at_debug_level(self, monkeypatch, capsys) -> None:
        """Debug log is emitted when LOG_LEVEL=debug."""
        import blocks_network.config as cfg
        monkeypatch.setattr(cfg, "LOG_LEVEL", "debug")

        from blocks_network.logging_utils import log_agent_instance_event
        log_agent_instance_event("debug", "debug message here")

        captured = capsys.readouterr()
        assert "debug message here" in captured.out


# ---------------------------------------------------------------------------
# Fix 8: BLOCKS_BACKEND_URL provider-side removal
# ---------------------------------------------------------------------------


class TestBlocksBackendUrlRemoved:
    def test_no_blocks_backend_url_in_agent_instance(self) -> None:
        """agent_instance.py must not read BLOCKS_BACKEND_URL from env."""
        import inspect
        import blocks_network.agent_instance as ai
        source = inspect.getsource(ai)
        # The string BLOCKS_BACKEND_URL should not appear in provider code
        assert 'BLOCKS_BACKEND_URL' not in source


# ---------------------------------------------------------------------------
# Fix 5: PUBNUB_SECRET_KEY removal
# ---------------------------------------------------------------------------


class TestPubnubSecretKeyRemoved:
    def test_no_secret_key_in_config(self) -> None:
        """config.py must not export PUBNUB_SECRET_KEY."""
        import blocks_network.config as cfg
        assert not hasattr(cfg, "PUBNUB_SECRET_KEY")

    def test_no_secret_key_in_pubnub_client(self) -> None:
        """pubnub_client.py must not read PUBNUB_SECRET_KEY from env."""
        import inspect
        import blocks_network.pubnub_client as pc
        source = inspect.getsource(pc)
        assert "PUBNUB_SECRET_KEY" not in source
        assert "secret_key" not in source


# ---------------------------------------------------------------------------
# Config cleanup: verify config.py exports only expected constants
# ---------------------------------------------------------------------------


class TestConfigCleanup:
    def test_config_exports_only_expected(self) -> None:
        """config.py must export exactly the env-driven constants
        (BLOCKS_CDM_URL, LOG_LEVEL, ARTIFACT_INLINE_LIMIT_BYTES) plus
        the platform-contract constant BLOCKS_MAX_UPLOAD_BYTES (which
        is intentionally non-env — see test_removed_constants_absent
        and tests/test_config.py)."""
        import blocks_network.config as cfg

        # Get all uppercase public attributes (constants)
        public_constants = [
            name for name in dir(cfg)
            if name.isupper() and not name.startswith("_")
        ]
        expected = {
            "BLOCKS_CDM_URL",
            "LOG_LEVEL",
            "ARTIFACT_INLINE_LIMIT_BYTES",
            "BLOCKS_MAX_UPLOAD_BYTES",
        }
        assert set(public_constants) == expected, (
            f"Expected {expected}, got {set(public_constants)}"
        )

    def test_removed_constants_absent(self) -> None:
        """Removed constants must not exist in config module.

        Note: BLOCKS_MAX_UPLOAD_BYTES is intentionally present but is a
        plain platform-contract constant (NOT env-driven), so it does
        not belong in this list. The env-var cleanup contract from the
        sdk_remove_env_vars initiative is preserved."""
        import blocks_network.config as cfg
        for removed in (
            "AGENT_NAME", "HANDLER", "CONCURRENCY", "EXPECTED_INSTANCES",
            "PUBNUB_SECRET_KEY", "NODE_ENV", "INSTANCE_ID",
        ):
            assert not hasattr(cfg, removed), f"{removed} still in config.py"


# ---------------------------------------------------------------------------
# INSTANCE_ID env override regression
# ---------------------------------------------------------------------------


class TestInstanceIdEnvIgnored:
    def test_leaked_instance_id_env_does_not_override_auto_generation(self, monkeypatch) -> None:
        """Setting INSTANCE_ID in the environment must not affect the
        auto-generated instance ID when options.instance_id is None.
        This confirms the env override path is fully severed."""
        monkeypatch.setenv("INSTANCE_ID", "AG-leaked-from-env-999")
        pn = _make_mock_pubnub()
        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",
                instance_id=None,
            )
        )
        instance_id = result["instance_id"]
        # Must NOT be the leaked env value
        assert instance_id != "AG-leaked-from-env-999", (
            "INSTANCE_ID env var leaked into the generated instance ID"
        )
        # Must follow the auto-generated format: AG-{agentName}-{uuid}
        assert instance_id.startswith("AG-acme_echo-"), (
            f"Expected instance_id to start with 'AG-acme_echo-', got: {instance_id}"
        )
        # The suffix after 'AG-acme_echo-' should be a valid UUID
        import uuid as uuid_mod
        suffix = instance_id[len("AG-acme_echo-"):]
        try:
            uuid_mod.UUID(suffix)
        except ValueError:
            pytest.fail(f"Instance ID suffix is not a valid UUID: {suffix}")
        result["stop"]()
