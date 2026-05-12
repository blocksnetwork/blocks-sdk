"""
PubNub client factory.

Creates a configured ``PubNub`` instance using the ``pubnub`` Python SDK.
Falls back gracefully if the ``pubnub`` package is not installed.
"""

from __future__ import annotations

import logging
import threading
from typing import Any, Optional

# Attempt import -- allow graceful failure so the rest of the package
# can be imported even without the pubnub dependency installed.
try:
    from pubnub.pnconfiguration import PNConfiguration
    from pubnub.pubnub import PubNub

    _PUBNUB_AVAILABLE = True
except ImportError:  # pragma: no cover
    _PUBNUB_AVAILABLE = False
    PNConfiguration = None  # type: ignore[assignment,misc]
    PubNub = None  # type: ignore[assignment,misc]


# BLOCKS-129 silent-park fix. Long-lived control clients raise the
# reconnection budget far above PubNub's default
# ExponentialDelay.MAX_RETRIES = 6 (~4-6min cumulative window with the
# 150s backoff cap). 43_200 ≈ 30 days at the 60s cap, matching the
# Node SDK's `subscribeRetryUnbounded` budget. The Python SDK exposes
# this as a flat int on PNConfiguration — there is no policy object
# to construct or validator to bypass. Setting maximum_reconnection_interval
# to 60s aligns the backoff cap with the Node SDK so log cadence is
# similar across runtimes.
_UNBOUNDED_MAX_RETRIES = 43_200
_UNBOUNDED_MAX_INTERVAL_S = 60


# Substring → category map for the pubnub.NativeReconnectionManager log
# messages we forward to the user callback. Source: pubnub/pubnub.py
# NativeReconnectionManager. Categories are stable across the on_retry
# contract; substrings are matched against pubnub's actual log text and
# may need updating across pubnub-python releases.
#
# - "retry": per-attempt DEBUG, fires every reconnect cycle under the
#   EXPONENTIAL policy. Visible "still trying" signal.
# - "recovered": DEBUG, fires once when an attempt finally succeeds and
#   the reconnection state machine resets _connection_errors to 1.
#   Without this, recovery is silent — observable only by the absence
#   of further retry messages, which is awkward for ops.
# - "failed": WARNING, fires once when the retry budget exhausts. With
#   subscribe_retry_unbounded=True the budget is 43_200 attempts so
#   this is not expected in normal operation; surfacing it makes a
#   regression that shrinks the budget unmissable.
_RETRY_LOG_CATEGORIES = (
    ("reconnect interval increment", "retry"),
    ("reconnection manager stop due success", "recovered"),
    ("Reconnection retry limit reached", "failed"),
)


class _RetryLogForwarder(logging.Handler):
    """Forwards PubNub reconnection log lines to a user callback.

    Installs at DEBUG so per-attempt retry messages (which the SDK logs
    at debug, not warn) reach emit(). The substring filter drops
    everything except the three reconnection-manager messages so the
    agent log stays focused even when the rest of pubnub debug logging
    is enabled.

    The callback receives (category, message). category is one of
    "retry", "recovered", "failed" — stable across pubnub releases.
    The raw message is included so the agent log can carry the
    underlying SDK text verbatim for forensic value.
    """

    def __init__(self, on_retry):
        super().__init__(level=logging.DEBUG)
        self._on_retry = on_retry

    def emit(self, record: logging.LogRecord) -> None:
        try:
            msg = record.getMessage()
        except Exception:
            return
        category = None
        for substring, cat in _RETRY_LOG_CATEGORIES:
            if substring in msg:
                category = cat
                break
        if category is None:
            return
        try:
            self._on_retry(category, msg)
        except Exception:
            # Never let a callback failure crash the logger thread.
            pass


# Sentinel attached to the PubNub instance so the patched stop() can
# find the handler we installed.
_BLOCKS_RETRY_HANDLER_ATTR = "_blocks_retry_handler"

# Module-level snapshot of the pubnub logger's level captured by the
# *first* installer (when no _RetryLogForwarder was yet attached). The
# last installer to be removed restores from this. A per-instance
# snapshot would be wrong because the second installer's "prior" level
# is already DEBUG (raised by the first installer); restoring from that
# would leave the logger stuck at DEBUG forever.
_blocks_retry_original_level: Optional[int] = None


class _BlocksRetryLevelFilter(logging.Filter):
    """Lets through retry-substring records (so the forwarder receives
    them) and any record at or above the host's original level (so host
    handlers continue to see what they already saw). Drops everything
    else BEFORE it reaches any handler or propagates to ancestors —
    this is what prevents the log-flood when we raise pubnub_logger to
    DEBUG so the forwarder can observe per-attempt retry messages.
    """

    def __init__(self, original_level: int) -> None:
        super().__init__()
        self._original_level = (
            logging.WARNING if original_level == logging.NOTSET else original_level
        )

    def filter(self, record: logging.LogRecord) -> bool:
        if record.levelno >= self._original_level:
            return True
        try:
            msg = record.getMessage()
        except Exception:
            return False
        return any(sub in msg for sub, _ in _RETRY_LOG_CATEGORIES)


# Module-level filter installed alongside the first forwarder. It
# drops non-retry pubnub DEBUG records at the logger so they cannot
# reach host handlers or propagate. Lifecycle is bound to
# _blocks_retry_original_level: installed when that snapshot is taken,
# removed when it is cleared.
_blocks_retry_level_filter: Optional[_BlocksRetryLevelFilter] = None


# Guards the compound (read pubnub_logger.handlers / decide
# is_first_installer / write module globals / install filter) sequence
# inside _install_retry_forwarder, and the symmetric teardown inside
# the patched _stop_with_cleanup. Python's logging module already
# locks individual addHandler / removeHandler calls, but the
# decide-then-write pattern across those calls is not atomic without
# an explicit lock.
_blocks_retry_install_lock = threading.Lock()


def _install_retry_forwarder(pn: Any, on_retry) -> None:
    """Install the retry-forwarder handler on the global ``pubnub``
    logger and bind its lifetime to ``pn``. The handler is removed
    (and the logger level restored if we were the last installer) when
    ``pn.stop()`` runs.

    The pre-fix version of this function added a handler with no
    removal path, which leaked across SwitchEnvironment cycles. The
    patch on ``pn.stop`` is idempotent — a second stop() call is a
    no-op because the sentinel attribute is cleared on first removal.
    """
    global _blocks_retry_original_level
    global _blocks_retry_level_filter

    handler = _RetryLogForwarder(on_retry)
    pubnub_logger = logging.getLogger("pubnub")

    with _blocks_retry_install_lock:
        # First-installer wins: only the install that finds zero existing
        # forwarders snapshots the logger's level. Later installs see DEBUG
        # (because the first one already lowered it) and must NOT overwrite.
        # The lock makes the check + global write + filter install atomic
        # across threads — without it, two threads can both observe no
        # forwarder, both call themselves the first installer, and both
        # add a _BlocksRetryLevelFilter that only the symmetric removal
        # in _stop_with_cleanup can pair with.
        is_first_installer = not any(
            isinstance(h, _RetryLogForwarder) for h in pubnub_logger.handlers
        )
        if is_first_installer:
            _blocks_retry_original_level = pubnub_logger.level
            _blocks_retry_level_filter = _BlocksRetryLevelFilter(pubnub_logger.level)
            pubnub_logger.addFilter(_blocks_retry_level_filter)

        pubnub_logger.addHandler(handler)
        setattr(pn, _BLOCKS_RETRY_HANDLER_ATTR, handler)

        if (
            pubnub_logger.level == logging.NOTSET
            or pubnub_logger.level > logging.DEBUG
        ):
            pubnub_logger.setLevel(logging.DEBUG)

    # Patch pn.stop() to clean up our handler. Idempotent.
    original_stop = pn.stop

    def _stop_with_cleanup(*args, **kwargs):  # type: ignore[no-untyped-def]
        global _blocks_retry_original_level
        global _blocks_retry_level_filter
        with _blocks_retry_install_lock:
            installed = getattr(pn, _BLOCKS_RETRY_HANDLER_ATTR, None)
            if installed is not None:
                try:
                    pubnub_logger.removeHandler(installed)
                except ValueError:
                    pass
                # If no other forwarder is still attached, restore the
                # original level captured by the first installer. Compare
                # by class so we don't trip on unrelated handlers a host
                # app may have attached.
                still_installed = any(
                    isinstance(h, _RetryLogForwarder)
                    for h in pubnub_logger.handlers
                )
                if not still_installed and _blocks_retry_original_level is not None:
                    pubnub_logger.setLevel(_blocks_retry_original_level)
                    _blocks_retry_original_level = None
                    if _blocks_retry_level_filter is not None:
                        try:
                            pubnub_logger.removeFilter(_blocks_retry_level_filter)
                        except ValueError:
                            pass
                        _blocks_retry_level_filter = None
                setattr(pn, _BLOCKS_RETRY_HANDLER_ATTR, None)
        return original_stop(*args, **kwargs)

    pn.stop = _stop_with_cleanup  # type: ignore[method-assign]


def _ensure_thread_safe_publish_sequence(pn: Any) -> None:
    """Wrap ``pn._publish_sequence_manager.get_next_sequence`` with a
    lock so concurrent publishes can't race it. Idempotent; no-op if
    the manager is already a thread-safe subclass or already wrapped;
    degrades to a no-op if the pubnub library's internal attribute
    layout changes. See PUBNUB_PYTHON_BUG_REPORT.md for context.
    """
    try:
        mgr = pn._publish_sequence_manager
    except AttributeError:
        return
    if mgr is None:
        return
    # Already wrapped by us on a prior call — idempotent guard.
    if getattr(mgr, "_blocks_thread_safe_seq", False):
        return
    # If the library has been fixed and is using the Native subclass
    # (which already locks), don't double-wrap.
    try:
        from pubnub.pubnub import NativePublishSequenceManager
        if isinstance(mgr, NativePublishSequenceManager):
            return
    except ImportError:
        pass  # Class moved or removed in a future release; fall through

    lock = threading.Lock()
    original_get_next = mgr.get_next_sequence

    def _thread_safe_get_next_sequence() -> int:
        with lock:
            return original_get_next()

    mgr.get_next_sequence = _thread_safe_get_next_sequence  # type: ignore[method-assign]
    mgr._blocks_thread_safe_seq = True  # type: ignore[attr-defined]


def create_pubnub_client(
    *,
    user_id: Optional[str] = None,
    publish_key: Optional[str] = None,
    subscribe_key: Optional[str] = None,
    presence_timeout: Optional[int] = None,
    subscribe_retry_unbounded: bool = True,
    on_retry=None,
) -> Any:
    """Create and return a configured :class:`pubnub.pubnub.PubNub` instance.

    Parameters are resolved from explicit arguments first, then from
    environment variables via :mod:`blocks_network.config`.

    Parameters
    ----------
    user_id:
        PubNub user ID (typically the instance ID).
    publish_key:
        PubNub publish key.
    subscribe_key:
        PubNub subscribe key.
    subscribe_retry_unbounded:
        Default: True. When True, raise the reconnection budget on the
        long-lived control client so the synchronous SDK never gives up
        after PubNub's default ~4-6min retry window exhausts. Per-task /
        ephemeral / per-stream call sites MUST pass
        ``subscribe_retry_unbounded=False`` explicitly to keep the
        fail-fast retry budget — a stuck task is preferable to a stuck
        loop. See dev_docs/SDK_CONTRACT.md §Cross-SDK retry-budget
        defaults for the contract.
    on_retry:
        BLOCKS-129. Optional callback invoked with ``(category, message)``
        for each PubNub reconnection log line we forward. Categories:

        - ``"retry"`` — per-attempt DEBUG, fires every reconnect cycle.
        - ``"recovered"`` — DEBUG, fires once when an attempt succeeds.
        - ``"failed"`` — WARNING, fires once when the retry budget exhausts.

        Wiring this turns the SDK's transport-layer reconnection activity
        into a visible signal so a human watching the agent log can tell
        the difference between "agent retrying", "agent recovered", and
        "agent dead". Filter is applied inside the handler; the callback
        only receives the three reconnection-manager messages, never the
        rest of pubnub's chatty DEBUG stream.

    Returns
    -------
    PubNub
        A configured PubNub client instance.

    Raises
    ------
    ImportError
        If the ``pubnub`` package is not installed.
    ValueError
        If ``subscribe_key`` is not provided (neither argument nor env).
    """
    if not _PUBNUB_AVAILABLE:
        raise ImportError(
            "The 'pubnub' package is required but not installed. "
            "Install it with: pip install pubnub>=10.6.0"
        )

    effective_user_id = user_id or "blocks-agent"
    effective_publish_key = publish_key or ""
    effective_subscribe_key = subscribe_key or ""

    if not effective_subscribe_key:
        raise ValueError(
            "subscribe_key is required: provide it as an argument or via CDM config"
        )

    pnconfig = PNConfiguration()
    pnconfig.subscribe_key = effective_subscribe_key
    pnconfig.user_id = effective_user_id
    pnconfig.daemon = True

    if effective_publish_key:
        pnconfig.publish_key = effective_publish_key

    if presence_timeout is not None:
        # IMPORTANT: pnconfig.presence_timeout is a @property with no
        # setter. Direct attribute assignment silently shadows the
        # property in __dict__ — the getter still returns the SDK
        # default (300s) and the broker keeps timing the UUID out at
        # ~5-10min on healthy agents. set_presence_timeout(...) writes
        # the underlying _presence_timeout AND derives _heartbeat_interval
        # as (timeout/2 - 1), which is what the SDK actually uses.
        pnconfig.set_presence_timeout(presence_timeout)
        # SECOND silent footgun: NativeSubscriptionManager.reconnect()
        # only spins up the heartbeat thread when
        # enable_presence_heartbeat is True (pubnub/pubnub.py line 413),
        # and PNConfiguration defaults this to False
        # (pnconfiguration.py line 40). Without flipping it, the
        # subscribe URL still carries heartbeat=N (so the broker expects
        # heartbeats every N seconds) but the SDK never actually sends
        # any — the broker then times the UUID out at exactly N seconds
        # on every healthy agent. Live evidence (2026-05-07): with
        # presence_timeout=20 alone, broker fired Action: timeout
        # ~20s after each Action: join. Setting this flag starts the
        # NativePeriodicCallback that fires _perform_heartbeat_loop
        # every _heartbeat_interval seconds.
        pnconfig.enable_presence_heartbeat = True

    if subscribe_retry_unbounded:
        # The synchronous Python SDK reads these flat ints directly
        # from PNConfiguration. When unset (None), it falls back to
        # the hardcoded ExponentialDelay constants in pubnub/managers.py.
        pnconfig.maximum_reconnection_retries = _UNBOUNDED_MAX_RETRIES
        pnconfig.maximum_reconnection_interval = _UNBOUNDED_MAX_INTERVAL_S

    pn = PubNub(pnconfig)
    _ensure_thread_safe_publish_sequence(pn)
    if on_retry is not None:
        _install_retry_forwarder(pn, on_retry)
    return pn
