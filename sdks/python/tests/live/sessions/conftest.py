import pytest

from tests.live.sessions.live_harness import LIVE, LIVE_API_KEY


@pytest.fixture(autouse=True)
def _live_api_key(monkeypatch: pytest.MonkeyPatch) -> None:
    """Undo the suite-wide stand-in key: live scenarios authenticate with the real one."""
    if LIVE:
        monkeypatch.setenv("BLOCKS_API_KEY", LIVE_API_KEY)
