"""
Tests for blocks_network.pubnub_client -- PubNub client factory.

Tests cover key validation, environment fallback, and configuration.
"""

from __future__ import annotations

import logging
from unittest.mock import MagicMock

import pytest


@pytest.fixture(autouse=True)
def _isolate_pubnub_logger():
    """Snapshot the pubnub logger's handlers and level around every
    test in this module so a leaking install can't propagate across
    tests. Required because _install_retry_forwarder mutates the
    global logging.getLogger('pubnub')."""
    pubnub_logger = logging.getLogger("pubnub")
    saved_handlers = pubnub_logger.handlers[:]
    saved_level = pubnub_logger.level
    saved_filters = pubnub_logger.filters[:]
    yield
    pubnub_logger.handlers[:] = saved_handlers
    pubnub_logger.setLevel(saved_level)
    pubnub_logger.filters[:] = saved_filters


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


def test_create_pubnub_client_does_not_override_retry_when_unbounded_false():
    """Per-task / per-stream / ephemeral path: when callers explicitly
    pass subscribe_retry_unbounded=False, create_pubnub_client MUST NOT
    touch the user-facing retry budget.

    Note: maximum_reconnection_retries being None does NOT mean no
    retry happens — when it's None, the synchronous PubNub SDK falls
    back to NativeReconnectionManager, which uses the hardcoded
    ExponentialDelay.MAX_RETRIES = 6 with MAX_BACKOFF = 150 seconds
    (pubnub/managers.py). That ~4-6min cumulative budget is the
    intentional fail-fast behavior for short-lived clients.

    The cross-SDK default for subscribe_retry_unbounded is True (see
    test_default_subscribe_retry_unbounded_is_true and the SDK's
    cross-SDK retry-budget defaults), so per-task / ephemeral call
    sites MUST opt out explicitly to retain this baseline behavior.

    Imports the real `pubnub` package — no mocks.
    """
    from blocks_network.pubnub_client import create_pubnub_client
    pn = create_pubnub_client(
        publish_key="pub-test",
        subscribe_key="sub-test",
        user_id="baseline-test",
        subscribe_retry_unbounded=False,
    )
    assert pn.config.maximum_reconnection_retries is None
    assert pn.config.maximum_reconnection_interval is None
    # Default subscribe long-poll timeout. The fix lowers this to 60s
    # only when subscribe_retry_unbounded=True; baseline must stay at
    # the SDK default.
    assert pn.config.subscribe_request_timeout == 310


def test_subscribe_retry_unbounded_raises_maximum_reconnection_retries_to_43200():
    """Mirrors the Node SDK fix shape: long-lived control client gets a
    30-day-equivalent retry budget so the synchronous SDK's
    NativeReconnectionManager never gives up. Implementation differs
    from Node — Python uses the flat
    PNConfiguration.maximum_reconnection_retries int, not a policy
    object. See Layer 1 header note.
    """
    from blocks_network.pubnub_client import create_pubnub_client
    pn = create_pubnub_client(
        publish_key="pub-test",
        subscribe_key="sub-test",
        user_id="retry-on",
        subscribe_retry_unbounded=True,
    )
    assert pn.config.maximum_reconnection_retries == 43_200
    # Pin the backoff cap to 60s so logs feel similar to the Node SDK
    # (Node default ExponentialRetryPolicy uses 60s).
    assert pn.config.maximum_reconnection_interval == 60
    # subscribe_request_timeout intentionally left at the SDK default
    # (310s). A previous attempt lowered this to 60s on long-lived
    # control clients to shrink the post-blip recovery gap, but the
    # cap also fires on every healthy long-poll — the broker observed
    # each client-side abort as a clean socket close and emitted a
    # presence `leave` every 60s on idle agents. A heartbeat-driven
    # recovery approach replaces this knob.
    assert pn.config.subscribe_request_timeout == 310


def test_subscribe_retry_unbounded_false_leaves_retry_unset():
    """Per-task / per-stream clients keep the SDK default — when
    maximum_reconnection_retries is None the SDK falls back to
    ExponentialDelay.MAX_RETRIES = 6, which is the right behavior
    for short-lived clients (a stuck task should fail cleanly). Same
    rationale for subscribe_request_timeout: per-task clients should
    surface a real subscribe stall as a task failure, not heal it
    aggressively.
    """
    from blocks_network.pubnub_client import create_pubnub_client
    pn = create_pubnub_client(
        publish_key="pub-test",
        subscribe_key="sub-test",
        user_id="retry-off",
        subscribe_retry_unbounded=False,
    )
    assert pn.config.maximum_reconnection_retries is None
    # subscribe_request_timeout is also untouched — pubnub default 310s.
    assert pn.config.subscribe_request_timeout == 310


def test_on_retry_callback_receives_per_attempt_debug_message():
    """When an on_retry callback is wired, PubNub's per-retry DEBUG
    'reconnect interval increment at: ...' line must reach it as
    category 'retry'. We drive a synthetic record directly through the
    installed handler so the test is hermetic (no real network).
    """
    captured: list[tuple[str, str]] = []
    from blocks_network.pubnub_client import create_pubnub_client
    create_pubnub_client(
        publish_key="pub-test",
        subscribe_key="sub-test",
        user_id="retry-cb-attempt",
        on_retry=lambda category, msg: captured.append((category, msg)),
    )
    import logging
    pubnub_logger = logging.getLogger("pubnub")
    record = pubnub_logger.makeRecord(
        name="pubnub",
        level=logging.DEBUG,
        fn="reconnection",
        lno=0,
        msg="reconnect interval increment at: 2026-05-07 14:38:15.500",
        args=(),
        exc_info=None,
    )
    for handler in pubnub_logger.handlers:
        handler.handle(record)
    assert captured == [
        ("retry", "reconnect interval increment at: 2026-05-07 14:38:15.500"),
    ]


def test_on_retry_callback_receives_recovered_debug_message():
    """The 'reconnection manager stop due success ...' DEBUG fires once
    when a reconnect attempt succeeds and the SDK transitions back to
    healthy. Forwarding it as category 'recovered' gives ops a positive
    signal that recovery happened — without it, recovery is observable
    only as the absence of further retry messages.
    """
    captured: list[tuple[str, str]] = []
    from blocks_network.pubnub_client import create_pubnub_client
    create_pubnub_client(
        publish_key="pub-test",
        subscribe_key="sub-test",
        user_id="retry-cb-recovered",
        on_retry=lambda category, msg: captured.append((category, msg)),
    )
    import logging
    pubnub_logger = logging.getLogger("pubnub")
    record = pubnub_logger.makeRecord(
        name="pubnub",
        level=logging.DEBUG,
        fn="reconnection",
        lno=0,
        msg="reconnection manager stop due success time endpoint call: 2026-05-07 14:39:00",
        args=(),
        exc_info=None,
    )
    for handler in pubnub_logger.handlers:
        handler.handle(record)
    assert captured == [
        (
            "recovered",
            "reconnection manager stop due success time endpoint call: 2026-05-07 14:39:00",
        ),
    ]


def test_on_retry_callback_receives_retry_limit_reached_warning():
    """The terminal 'Reconnection retry limit reached. Disconnecting.'
    WARNING fires once when the budget exhausts. Forwarded as category
    'failed' so a regression that shrinks the retry budget is loudly
    surfaced in the agent log.
    """
    captured: list[tuple[str, str]] = []
    from blocks_network.pubnub_client import create_pubnub_client
    create_pubnub_client(
        publish_key="pub-test",
        subscribe_key="sub-test",
        user_id="retry-cb-terminal",
        on_retry=lambda category, msg: captured.append((category, msg)),
    )
    import logging
    pubnub_logger = logging.getLogger("pubnub")
    record = pubnub_logger.makeRecord(
        name="pubnub",
        level=logging.WARNING,
        fn="reconnection",
        lno=0,
        msg="Reconnection retry limit reached. Disconnecting.",
        args=(),
        exc_info=None,
    )
    for handler in pubnub_logger.handlers:
        handler.handle(record)
    assert captured == [
        ("failed", "Reconnection retry limit reached. Disconnecting."),
    ]


def test_default_subscribe_retry_unbounded_is_true():
    """Cross-SDK parity contract: Python and Node both default the
    unbounded-retry flag to True. Per-task / ephemeral / per-stream
    call sites MUST opt OUT explicitly."""
    import inspect

    from blocks_network.pubnub_client import create_pubnub_client

    sig = inspect.signature(create_pubnub_client)
    param = sig.parameters["subscribe_retry_unbounded"]
    assert param.default is True, (
        "subscribe_retry_unbounded default must be True to match Node SDK; "
        "per-task / ephemeral callers MUST pass subscribe_retry_unbounded=False "
        "explicitly to keep the fail-fast retry budget."
    )


def test_on_retry_callback_ignores_unrelated_messages():
    """The handler is installed at DEBUG level so it sees the chatty
    pubnub debug stream; the substring filter MUST drop everything that
    isn't one of the three reconnection-manager events so the agent log
    stays focused.
    """
    captured: list[tuple[str, str]] = []
    from blocks_network.pubnub_client import create_pubnub_client
    create_pubnub_client(
        publish_key="pub-test",
        subscribe_key="sub-test",
        user_id="retry-filter",
        on_retry=lambda category, msg: captured.append((category, msg)),
    )
    import logging
    pubnub_logger = logging.getLogger("pubnub")
    for level, msg in [
        (logging.DEBUG, "reconnection manager start at: 2026-05-07 14:33:00"),
        (logging.DEBUG, "subscribe call to https://ps.pndsn.com/..."),
        (logging.INFO, "subscribe pn_async dispatched"),
        (logging.WARNING, "some other warning"),
    ]:
        record = pubnub_logger.makeRecord(
            name="pubnub", level=level, fn="x", lno=0,
            msg=msg, args=(), exc_info=None,
        )
        for handler in pubnub_logger.handlers:
            handler.handle(record)
    assert captured == []


def test_retry_forwarder_does_not_leak_across_client_constructions():
    """Each create_pubnub_client(..., on_retry=cb) MUST clean up its
    handler when the returned PubNub instance is stopped. Otherwise
    switchEnvironment cycles accumulate handlers and the SDK delivers
    each retry log line N times to overlapping callbacks."""
    from blocks_network.pubnub_client import create_pubnub_client

    pubnub_logger = logging.getLogger("pubnub")
    starting_handler_count = len(pubnub_logger.handlers)

    pn1 = create_pubnub_client(
        publish_key="pub-x",
        subscribe_key="sub-x",
        user_id="u1",
        on_retry=lambda *_: None,
    )
    pn2 = create_pubnub_client(
        publish_key="pub-x",
        subscribe_key="sub-x",
        user_id="u2",
        on_retry=lambda *_: None,
    )

    # Both clients are alive — both handlers present.
    assert len(pubnub_logger.handlers) == starting_handler_count + 2

    # Stop the first client; its handler must be removed.
    pn1.stop()
    assert len(pubnub_logger.handlers) == starting_handler_count + 1

    # Stop the second; we should be back to the starting state.
    pn2.stop()
    assert len(pubnub_logger.handlers) == starting_handler_count


def test_retry_forwarder_restores_logger_level_on_last_handler_removal():
    """When the last forwarder is removed, the logger's level must be
    restored to whatever it was before the first install."""
    from blocks_network.pubnub_client import create_pubnub_client

    pubnub_logger = logging.getLogger("pubnub")
    pubnub_logger.setLevel(logging.WARNING)  # known starting state

    pn = create_pubnub_client(
        publish_key="pub-x",
        subscribe_key="sub-x",
        user_id="u",
        on_retry=lambda *_: None,
    )
    assert pubnub_logger.level == logging.DEBUG  # raised by install

    pn.stop()
    assert pubnub_logger.level == logging.WARNING  # restored


def test_retry_forwarder_restores_original_level_across_two_clients():
    """Regression: with two live clients, the second installer must NOT
    overwrite the first installer's prior-level snapshot. If it did,
    stopping in last-in-last-out order would restore from `DEBUG` (the
    state the second installer saw) instead of the original level (e.g.
    `WARNING`), leaving the global logger stuck at DEBUG.

    Mirrors the production scenario in agent_instance.py.switchEnvironment,
    which constructs a second control client while the first is still
    alive."""
    from blocks_network.pubnub_client import create_pubnub_client

    pubnub_logger = logging.getLogger("pubnub")
    pubnub_logger.setLevel(logging.WARNING)  # original level

    pn1 = create_pubnub_client(
        publish_key="pub-x",
        subscribe_key="sub-x",
        user_id="u1",
        on_retry=lambda *_: None,
    )
    assert pubnub_logger.level == logging.DEBUG

    pn2 = create_pubnub_client(
        publish_key="pub-x",
        subscribe_key="sub-x",
        user_id="u2",
        on_retry=lambda *_: None,
    )
    # Second installer sees DEBUG; must not overwrite the snapshot.
    assert pubnub_logger.level == logging.DEBUG

    pn1.stop()
    # One forwarder still installed (pn2's); level stays DEBUG.
    assert pubnub_logger.level == logging.DEBUG

    pn2.stop()
    # Last forwarder removed; restore to the FIRST installer's snapshot
    # (WARNING), not the second installer's (DEBUG).
    assert pubnub_logger.level == logging.WARNING


def test_retry_forwarder_does_not_flood_sibling_handlers_with_non_retry_debug():
    """Regression: installing the retry forwarder must NOT cause unrelated
    pubnub DEBUG records to reach handlers attached to the pubnub logger
    or its ancestors. Pre-fix, the install raised pubnub_logger.level to
    DEBUG, so every pubnub debug record propagated to host handlers — a
    log-flood for any agent host using stdlib logging at root.
    """
    from blocks_network.pubnub_client import create_pubnub_client

    pubnub_logger = logging.getLogger("pubnub")
    pubnub_logger.setLevel(logging.WARNING)  # host's original level

    captured: list[logging.LogRecord] = []

    class _Capture(logging.Handler):
        def __init__(self) -> None:
            super().__init__(level=logging.DEBUG)

        def emit(self, record: logging.LogRecord) -> None:
            captured.append(record)

    sibling = _Capture()
    pubnub_logger.addHandler(sibling)

    try:
        pn = create_pubnub_client(
            publish_key="pub-x",
            subscribe_key="sub-x",
            user_id="u",
            on_retry=lambda *_: None,
        )

        # Emit a non-retry DEBUG record on the pubnub logger.
        pubnub_logger.debug("some unrelated pubnub debug noise")
        # Emit a known retry DEBUG record (must still pass through to the
        # forwarder's path; sibling capturing it is incidental and fine).
        pubnub_logger.debug("reconnect interval increment to 30s")

        pn.stop()
    finally:
        pubnub_logger.removeHandler(sibling)

    non_retry_records = [
        r for r in captured if "unrelated pubnub debug noise" in r.getMessage()
    ]
    assert non_retry_records == [], (
        "non-retry pubnub DEBUG records leaked to sibling handler; "
        "the retry forwarder must not flood unrelated handlers"
    )


def test_concurrent_install_and_teardown_is_thread_safe(monkeypatch) -> None:
    """Two threads racing _install_retry_forwarder must observe exactly
    one "first installer". After both clients run stop(), the pubnub
    logger MUST be fully restored — no leaked forwarder, no leaked
    filter, level reverted to its pre-install value.

    Today, the is_first_installer check + module-global write is not
    atomic, so a race can leave the filter list desynchronised from the
    handler list (both threads write, only one removal path runs)."""
    import logging
    import threading
    import blocks_network.pubnub_client as pc

    class _FakePN:
        def __init__(self, cfg):
            self._cfg = cfg
            self._publish_sequence_manager = None

        def stop(self):
            pass

    class _FakePNConfiguration:
        def __init__(self):
            self.subscribe_key = ""
            self.publish_key = ""
            self.user_id = ""
            self.daemon = False
            self.maximum_reconnection_retries = None
            self.maximum_reconnection_interval = None

    monkeypatch.setattr(pc, "PubNub", _FakePN)
    monkeypatch.setattr(pc, "PNConfiguration", _FakePNConfiguration)
    monkeypatch.setattr(pc, "_PUBNUB_AVAILABLE", True)

    pubnub_logger = logging.getLogger("pubnub")
    pubnub_logger.setLevel(logging.WARNING)

    pre_handlers = list(pubnub_logger.handlers)
    pre_filters = list(pubnub_logger.filters)

    clients: list = []
    errors: list[BaseException] = []
    start_barrier = threading.Barrier(2)

    def _spawn() -> None:
        try:
            start_barrier.wait(timeout=5)
            client = pc.create_pubnub_client(
                subscribe_key="sub-key",
                publish_key="pub-key",
                on_retry=lambda *_: None,
            )
            clients.append(client)
        except BaseException as exc:  # noqa: BLE001 - reraised below
            errors.append(exc)

    t1 = threading.Thread(target=_spawn)
    t2 = threading.Thread(target=_spawn)
    t1.start()
    t2.start()
    t1.join(timeout=5)
    t2.join(timeout=5)
    assert not errors, f"thread errors: {errors!r}"
    assert len(clients) == 2

    # Exactly two forwarders installed (one per client) and exactly one
    # level filter (only the first installer adds one).
    forwarders = [h for h in pubnub_logger.handlers if isinstance(h, pc._RetryLogForwarder)]
    level_filters = [f for f in pubnub_logger.filters if isinstance(f, pc._BlocksRetryLevelFilter)]
    assert len(forwarders) == 2, (
        f"expected 2 forwarders, got {len(forwarders)}"
    )
    assert len(level_filters) == 1, (
        f"expected exactly 1 _BlocksRetryLevelFilter installed by the "
        f"first installer, got {len(level_filters)}"
    )

    # Tear both clients down. The last-stop must restore the logger
    # fully: no forwarder left, no _BlocksRetryLevelFilter left, level
    # reverted to WARNING.
    for client in clients:
        client.stop()

    remaining_forwarders = [
        h for h in pubnub_logger.handlers if isinstance(h, pc._RetryLogForwarder)
    ]
    remaining_filters = [
        f for f in pubnub_logger.filters if isinstance(f, pc._BlocksRetryLevelFilter)
    ]
    assert remaining_forwarders == [], (
        f"forwarder leaked after stop(): {remaining_forwarders!r}"
    )
    assert remaining_filters == [], (
        f"level filter leaked after stop(): {remaining_filters!r}"
    )
    assert pubnub_logger.level == logging.WARNING, (
        f"pubnub logger level not restored: {pubnub_logger.level}"
    )
    # Module globals must also be cleared.
    assert pc._blocks_retry_original_level is None
    assert pc._blocks_retry_level_filter is None

    # Restore the surrounding test state. The autouse fixture handles
    # handlers/level/filters; these are belt-and-braces for clarity.
    assert pre_handlers == pubnub_logger.handlers
    assert pre_filters == pubnub_logger.filters


class TestQuietTransportLoggers:
    @pytest.fixture(autouse=True)
    def _isolate_transport_loggers(self):
        """Snapshot httpx/httpcore logger level + filters around each test
        so _quiet_transport_loggers' global mutation can't leak across
        tests."""
        import blocks_network.pubnub_client as pc

        saved = {
            name: (
                logging.getLogger(name).level,
                list(logging.getLogger(name).filters),
            )
            for name in pc._TRANSPORT_LOGGER_NAMES
        }
        yield
        for name, (level, filters) in saved.items():
            logger = logging.getLogger(name)
            logger.setLevel(level)
            logger.filters = filters

    @staticmethod
    def _record(name: str, message: str, level: int = logging.INFO):
        return logging.LogRecord(
            name=name,
            level=level,
            pathname=__file__,
            lineno=0,
            msg=message,
            args=(),
            exc_info=None,
        )

    def test_installs_filter_that_drops_pubnub_request_lines(
        self, monkeypatch
    ) -> None:
        """By default (no forward_transport opt-in) a content filter is
        installed that drops PubNub-origin per-request INFO lines while
        leaving the logger's level untouched."""
        import blocks_network.pubnub_client as pc

        monkeypatch.delenv("BLOCKS_DEBUG_INTERNAL", raising=False)

        pc._quiet_transport_loggers()

        for name in pc._TRANSPORT_LOGGER_NAMES:
            logger = logging.getLogger(name)
            installed = [
                f
                for f in logger.filters
                if isinstance(f, pc._PubNubTransportFilter)
            ]
            assert len(installed) == 1
            pubnub_line = self._record(
                name, "HTTP Request: GET https://ps.pndsn.com/v2/subscribe 200 OK"
            )
            assert installed[0].filter(pubnub_line) is False

    def test_filter_passes_unrelated_application_http_logs(
        self, monkeypatch
    ) -> None:
        """The shared httpx/httpcore loggers are process-global; the filter
        must let a host app's own (non-PubNub) request lines through."""
        import blocks_network.pubnub_client as pc

        monkeypatch.delenv("BLOCKS_DEBUG_INTERNAL", raising=False)

        pc._quiet_transport_loggers()

        transport_filter = next(
            f
            for f in logging.getLogger("httpx").filters
            if isinstance(f, pc._PubNubTransportFilter)
        )
        app_line = self._record(
            "httpx", "HTTP Request: GET https://api.example.com/v1/users 200 OK"
        )
        assert transport_filter.filter(app_line) is True

    def test_filter_passes_pubnub_warnings_and_errors(self, monkeypatch) -> None:
        """Genuine transport errors must surface even for PubNub hosts —
        only sub-WARNING chatter is suppressed."""
        import blocks_network.pubnub_client as pc

        monkeypatch.delenv("BLOCKS_DEBUG_INTERNAL", raising=False)

        pc._quiet_transport_loggers()

        transport_filter = next(
            f
            for f in logging.getLogger("httpx").filters
            if isinstance(f, pc._PubNubTransportFilter)
        )
        warning = self._record(
            "httpx",
            "connect failed to ps.pndsn.com",
            level=logging.WARNING,
        )
        assert transport_filter.filter(warning) is True

    def test_idempotent_install(self, monkeypatch) -> None:
        """Repeated client construction must not stack duplicate filters."""
        import blocks_network.pubnub_client as pc

        monkeypatch.delenv("BLOCKS_DEBUG_INTERNAL", raising=False)

        pc._quiet_transport_loggers()
        pc._quiet_transport_loggers()

        for name in pc._TRANSPORT_LOGGER_NAMES:
            installed = [
                f
                for f in logging.getLogger(name).filters
                if isinstance(f, pc._PubNubTransportFilter)
            ]
            assert len(installed) == 1

    def test_does_not_mutate_logger_level(self, monkeypatch) -> None:
        """The fix must never touch the shared loggers' level."""
        import blocks_network.pubnub_client as pc

        monkeypatch.delenv("BLOCKS_DEBUG_INTERNAL", raising=False)
        for name in pc._TRANSPORT_LOGGER_NAMES:
            logging.getLogger(name).setLevel(logging.NOTSET)

        pc._quiet_transport_loggers()

        for name in pc._TRANSPORT_LOGGER_NAMES:
            assert logging.getLogger(name).level == logging.NOTSET

    @staticmethod
    def _filter_count(pc, name: str) -> int:
        return sum(
            isinstance(f, pc._PubNubTransportFilter)
            for f in logging.getLogger(name).filters
        )

    def test_forward_transport_installs_no_filter(self, monkeypatch) -> None:
        """With BLOCKS_DEBUG_INTERNAL=forward_transport the raw request
        stream stays visible — the call installs no filter (mirrors the
        Node SDK opt-in)."""
        import blocks_network.pubnub_client as pc

        monkeypatch.setenv("BLOCKS_DEBUG_INTERNAL", "forward_transport")
        before = {n: self._filter_count(pc, n) for n in pc._TRANSPORT_LOGGER_NAMES}

        pc._quiet_transport_loggers()

        for name in pc._TRANSPORT_LOGGER_NAMES:
            assert self._filter_count(pc, name) == before[name]

    def test_forward_transport_among_multiple_tokens(self, monkeypatch) -> None:
        """The token is matched within a comma-separated list, same
        format as Node's BLOCKS_DEBUG_INTERNAL."""
        import blocks_network.pubnub_client as pc

        monkeypatch.setenv("BLOCKS_DEBUG_INTERNAL", "diagnostics, forward_transport")
        before = {n: self._filter_count(pc, n) for n in pc._TRANSPORT_LOGGER_NAMES}

        pc._quiet_transport_loggers()

        for name in pc._TRANSPORT_LOGGER_NAMES:
            assert self._filter_count(pc, name) == before[name]

    def test_unrelated_token_still_suppresses(self, monkeypatch) -> None:
        """An unrelated debug token does not enable transport logs;
        LOG_LEVEL=debug also does not (it is not the gate)."""
        import blocks_network.pubnub_client as pc

        monkeypatch.setenv("BLOCKS_DEBUG_INTERNAL", "diagnostics")
        monkeypatch.setattr(pc._cfg, "LOG_LEVEL", "debug")

        pc._quiet_transport_loggers()

        for name in pc._TRANSPORT_LOGGER_NAMES:
            assert any(
                isinstance(f, pc._PubNubTransportFilter)
                for f in logging.getLogger(name).filters
            )
