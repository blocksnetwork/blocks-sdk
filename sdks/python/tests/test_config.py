"""
Tests for blocks_network.config — platform-contract constants.

The env-driven exports (BLOCKS_CDM_URL, LOG_LEVEL,
ARTIFACT_INLINE_LIMIT_BYTES) are covered by test_env_var_removal.py.
This file covers the plain (non-env) platform-contract constants.
"""

from __future__ import annotations


class TestBlocksMaxUploadBytes:
    def test_value_is_25_mib(self) -> None:
        """BLOCKS_MAX_UPLOAD_BYTES must equal 25 * 1024 * 1024 (25 MiB)
        — must match the service's MAX_FILE_SIZE_BYTES."""
        from blocks_network.config import BLOCKS_MAX_UPLOAD_BYTES

        assert BLOCKS_MAX_UPLOAD_BYTES == 26_214_400
        assert BLOCKS_MAX_UPLOAD_BYTES == 25 * 1024 * 1024

    def test_exported_from_package_root(self) -> None:
        """BLOCKS_MAX_UPLOAD_BYTES must be importable from blocks_network."""
        from blocks_network import BLOCKS_MAX_UPLOAD_BYTES as from_root
        from blocks_network.config import BLOCKS_MAX_UPLOAD_BYTES as from_config

        assert from_root == from_config == 26_214_400

    def test_listed_in_dunder_all(self) -> None:
        """BLOCKS_MAX_UPLOAD_BYTES must appear in blocks_network.__all__."""
        import blocks_network

        assert "BLOCKS_MAX_UPLOAD_BYTES" in blocks_network.__all__

    def test_is_plain_int_not_env_driven(self, monkeypatch) -> None:
        """The constant must not be readable from BLOCKS_MAX_UPLOAD_BYTES env;
        it is a fixed platform contract."""
        monkeypatch.setenv("BLOCKS_MAX_UPLOAD_BYTES", "9999")
        # Re-import the module to confirm the env var is ignored.
        import importlib

        import blocks_network.config as cfg

        importlib.reload(cfg)
        assert cfg.BLOCKS_MAX_UPLOAD_BYTES == 26_214_400
