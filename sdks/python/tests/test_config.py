"""
Tests for blocks_network.config — platform-contract constants.

The env-driven exports (BLOCKS_CDM_URL, LOG_LEVEL,
ARTIFACT_INLINE_LIMIT_BYTES) are covered by test_env_var_removal.py.
This file covers the plain (non-env) platform-contract constants.
"""

from __future__ import annotations


class TestBlocksMaxUploadBytes:
    def test_exported_from_package_root(self) -> None:
        """BLOCKS_MAX_UPLOAD_BYTES must be importable from blocks_network."""
        from blocks_network import BLOCKS_MAX_UPLOAD_BYTES as from_root
        from blocks_network.config import BLOCKS_MAX_UPLOAD_BYTES as from_config

        assert from_root == from_config

    def test_listed_in_dunder_all(self) -> None:
        """BLOCKS_MAX_UPLOAD_BYTES must appear in blocks_network.__all__."""
        import blocks_network

        assert "BLOCKS_MAX_UPLOAD_BYTES" in blocks_network.__all__

    def test_is_plain_int_not_env_driven(self, monkeypatch) -> None:
        """The constant must not be readable from BLOCKS_MAX_UPLOAD_BYTES env;
        it is a fixed platform contract."""
        import importlib

        import blocks_network.config as cfg

        before = cfg.BLOCKS_MAX_UPLOAD_BYTES
        monkeypatch.setenv("BLOCKS_MAX_UPLOAD_BYTES", "9999")
        # Re-import the module to confirm the env var is ignored.
        importlib.reload(cfg)
        assert cfg.BLOCKS_MAX_UPLOAD_BYTES == before


class TestDebugSubsystemEnabled:
    def test_false_when_unset(self, monkeypatch) -> None:
        from blocks_network import config as cfg

        monkeypatch.delenv("BLOCKS_DEBUG_INTERNAL", raising=False)
        assert cfg.debug_subsystem_enabled("forward_transport") is False

    def test_true_for_exact_token(self, monkeypatch) -> None:
        from blocks_network import config as cfg

        monkeypatch.setenv("BLOCKS_DEBUG_INTERNAL", "forward_transport")
        assert cfg.debug_subsystem_enabled("forward_transport") is True

    def test_matches_within_comma_list_with_whitespace(self, monkeypatch) -> None:
        from blocks_network import config as cfg

        monkeypatch.setenv("BLOCKS_DEBUG_INTERNAL", "diagnostics, forward_transport ")
        assert cfg.debug_subsystem_enabled("forward_transport") is True

    def test_false_for_substring_only(self, monkeypatch) -> None:
        """Tokens are matched whole, not as substrings."""
        from blocks_network import config as cfg

        monkeypatch.setenv("BLOCKS_DEBUG_INTERNAL", "forward_transport_extra")
        assert cfg.debug_subsystem_enabled("forward_transport") is False

    def test_false_for_unrelated_token(self, monkeypatch) -> None:
        from blocks_network import config as cfg

        monkeypatch.setenv("BLOCKS_DEBUG_INTERNAL", "diagnostics")
        assert cfg.debug_subsystem_enabled("forward_transport") is False
