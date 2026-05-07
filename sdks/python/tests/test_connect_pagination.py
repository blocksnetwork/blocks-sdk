"""Tests for connect() history pagination in TaskClient."""

from __future__ import annotations

import json
from unittest.mock import MagicMock, patch, call

import pytest

from blocks_network.task_client import TaskClient
from blocks_network.auth_provider import StaticAuthProvider


def _make_urlopen_response(body: dict) -> MagicMock:
    resp = MagicMock()
    encoded = json.dumps(body).encode("utf-8")
    resp.read.return_value = encoded
    resp.__enter__ = MagicMock(return_value=resp)
    resp.__exit__ = MagicMock(return_value=False)
    return resp


# PubNub timetokens are 17-digit integers. Use a realistic base
# to ensure string comparison works correctly.
_TT_BASE = 17000000000000000


def _make_history_message(msg_type: str, timetoken: int, extra: dict = None) -> MagicMock:
    """Create a mock PubNub history message."""
    m = MagicMock()
    payload = {"type": msg_type, "taskId": "t1"}
    if extra:
        payload.update(extra)
    m.message = payload
    m.timetoken = timetoken
    return m


class TestConnectPagination:
    """Test that _fetch_and_parse_history paginates correctly."""

    def test_single_page_no_pagination(self):
        """When fewer than 100 messages, no pagination needed."""
        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="free",
            base_url="http://api.test",
            auth_provider=StaticAuthProvider("jwt"),
        )

        mock_pn = MagicMock()
        # Return 3 messages (less than page size)
        messages = [
            _make_history_message("progress", _TT_BASE + 100),
            _make_history_message("progress", _TT_BASE + 200),
            _make_history_message("artifact", _TT_BASE + 300, {
                "artifactRef": {
                    "type": "inline",
                    "mimeType": "text/plain",
                    "data": "dGVzdA==",
                }
            }),
        ]

        fetch_chain = MagicMock()
        mock_pn.fetch_messages.return_value = fetch_chain
        fetch_chain.channels.return_value = fetch_chain
        fetch_chain.maximum_per_channel.return_value = fetch_chain
        fetch_chain.start.return_value = fetch_chain

        result_obj = MagicMock()
        result_obj.result.channels = {"u.org.t1": messages}
        fetch_chain.sync.return_value = result_obj

        streams, artifacts, hwm, _terminal = client._fetch_and_parse_history(
            mock_pn, "u.org.t1", "echo", "t1",
            {"subscribe_key": "sub-c-test", "publish_key": ""},
        )

        assert len(artifacts) == 1
        assert hwm == str(_TT_BASE + 300)
        # fetch_messages called once (no pagination)
        assert mock_pn.fetch_messages.call_count == 1

    def test_multi_page_pagination(self):
        """When exactly 100 messages on first page, fetches second page.

        PubNub fetchMessages returns most recent messages first. Page 1
        has newer timetokens, page 2 has older ones. The cursor is the
        oldest timetoken from page 1 (start is exclusive, returns older).
        After collection, messages are sorted ascending for replay.
        """
        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="free",
            base_url="http://api.test",
            auth_provider=StaticAuthProvider("jwt"),
        )

        mock_pn = MagicMock()

        # Page 1: 100 most recent messages (ascending within page)
        page1 = [_make_history_message("progress", _TT_BASE + 10000 + i * 100) for i in range(100)]
        # Page 2: 5 older messages (lower timetokens)
        page2 = [_make_history_message("progress", _TT_BASE + i * 100) for i in range(5)]

        fetch_call_count = [0]
        captured_starts = []
        fetch_chain = MagicMock()
        mock_pn.fetch_messages.return_value = fetch_chain
        fetch_chain.channels.return_value = fetch_chain
        fetch_chain.maximum_per_channel.return_value = fetch_chain

        def capture_start(val):
            captured_starts.append(val)
            return fetch_chain

        fetch_chain.start.side_effect = capture_start

        def sync_side_effect():
            fetch_call_count[0] += 1
            result_obj = MagicMock()
            if fetch_call_count[0] == 1:
                result_obj.result.channels = {"u.org.t1": page1}
            else:
                result_obj.result.channels = {"u.org.t1": page2}
            return result_obj

        fetch_chain.sync.side_effect = sync_side_effect

        streams, artifacts, hwm, _terminal = client._fetch_and_parse_history(
            mock_pn, "u.org.t1", "echo", "t1",
            {"subscribe_key": "sub-c-test", "publish_key": ""},
        )

        # Two pages fetched
        assert fetch_call_count[0] == 2
        # Cursor for page 2 should be oldest timetoken from page 1
        assert captured_starts == [_TT_BASE + 10000]
        # High water mark is the highest timetoken across all pages
        assert hwm == str(_TT_BASE + 10000 + 99 * 100)

    def test_empty_history(self):
        """When history is empty, returns defaults."""
        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="free",
            base_url="http://api.test",
            auth_provider=StaticAuthProvider("jwt"),
        )

        mock_pn = MagicMock()
        fetch_chain = MagicMock()
        mock_pn.fetch_messages.return_value = fetch_chain
        fetch_chain.channels.return_value = fetch_chain
        fetch_chain.maximum_per_channel.return_value = fetch_chain
        fetch_chain.start.return_value = fetch_chain

        result_obj = MagicMock()
        result_obj.result.channels = {}
        fetch_chain.sync.return_value = result_obj

        streams, artifacts, hwm, _terminal = client._fetch_and_parse_history(
            mock_pn, "u.org.t1", "echo", "t1",
            {"subscribe_key": "sub-c-test", "publish_key": ""},
        )

        assert len(streams) == 0
        assert len(artifacts) == 0
        assert hwm == "0"

    def test_pagination_stops_when_channel_missing(self):
        """When second page has no channel key, pagination stops."""
        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="free",
            base_url="http://api.test",
            auth_provider=StaticAuthProvider("jwt"),
        )

        mock_pn = MagicMock()

        # 100 messages — triggers pagination attempt
        page1 = [_make_history_message("progress", _TT_BASE + 10000 + i * 100) for i in range(100)]

        fetch_call_count = [0]
        fetch_chain = MagicMock()
        mock_pn.fetch_messages.return_value = fetch_chain
        fetch_chain.channels.return_value = fetch_chain
        fetch_chain.maximum_per_channel.return_value = fetch_chain
        fetch_chain.start.return_value = fetch_chain

        def sync_side_effect():
            fetch_call_count[0] += 1
            result_obj = MagicMock()
            if fetch_call_count[0] == 1:
                result_obj.result.channels = {"u.org.t1": page1}
            else:
                result_obj.result.channels = {}  # No messages on second page
            return result_obj

        fetch_chain.sync.side_effect = sync_side_effect

        streams, artifacts, hwm, _terminal = client._fetch_and_parse_history(
            mock_pn, "u.org.t1", "echo", "t1",
            {"subscribe_key": "sub-c-test", "publish_key": ""},
        )

        assert fetch_call_count[0] == 2
        # High water mark is the highest timetoken from page 1
        assert hwm == str(_TT_BASE + 10000 + 99 * 100)

    def test_stream_events_parsed_across_pages(self):
        """Stream started events on different pages are all parsed."""
        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="free",
            base_url="http://api.test",
            auth_provider=StaticAuthProvider("jwt"),
        )

        mock_pn = MagicMock()

        stream_msg_1 = _make_history_message("progress", _TT_BASE + 500, {
            "streamEvent": "stream_started",
            "streams": {
                "s1": {
                    "channel": "stream.echo.s1",
                    "token": "tok1",
                    "direction": "outbound",
                    "format": "events",
                    "affinity": "dedicated",
                }
            },
        })

        # Make sure the mock message attribute is a plain dict
        stream_msg_1.message = {
            "type": "progress",
            "taskId": "t1",
            "streamEvent": "stream_started",
            "streams": {
                "s1": {
                    "channel": "stream.echo.s1",
                    "token": "tok1",
                    "direction": "outbound",
                    "format": "events",
                    "affinity": "dedicated",
                }
            },
        }

        # Fill page to exactly 100
        page1_filler = [_make_history_message("progress", _TT_BASE + i) for i in range(1, 100)]
        page1 = page1_filler + [stream_msg_1]

        stream_msg_2 = MagicMock()
        stream_msg_2.message = {
            "type": "progress",
            "taskId": "t1",
            "streamEvent": "stream_started",
            "streams": {
                "s2": {
                    "channel": "stream.echo.s2",
                    "token": "tok2",
                    "direction": "outbound",
                    "format": "bytes",
                    "affinity": "dedicated",
                }
            },
        }
        stream_msg_2.timetoken = _TT_BASE + 600

        page2 = [stream_msg_2]

        fetch_call_count = [0]
        fetch_chain = MagicMock()
        mock_pn.fetch_messages.return_value = fetch_chain
        fetch_chain.channels.return_value = fetch_chain
        fetch_chain.maximum_per_channel.return_value = fetch_chain
        fetch_chain.start.return_value = fetch_chain

        def sync_side_effect():
            fetch_call_count[0] += 1
            result_obj = MagicMock()
            if fetch_call_count[0] == 1:
                result_obj.result.channels = {"u.org.t1": page1}
            else:
                result_obj.result.channels = {"u.org.t1": page2}
            return result_obj

        fetch_chain.sync.side_effect = sync_side_effect

        streams, artifacts, hwm, _terminal = client._fetch_and_parse_history(
            mock_pn, "u.org.t1", "echo", "t1",
            {"subscribe_key": "sub-c-test", "publish_key": ""},
        )

        assert "s1" in streams
        assert "s2" in streams
        assert hwm == str(_TT_BASE + 600)

    def test_declared_stream_extracted_from_history(self):
        """declaredStream field is plumbed into StreamDescriptor from history."""
        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="free",
            base_url="http://api.test",
            auth_provider=StaticAuthProvider("jwt"),
        )

        mock_pn = MagicMock()

        stream_msg = MagicMock()
        stream_msg.message = {
            "type": "progress",
            "taskId": "t1",
            "streamEvent": "stream_started",
            "declaredStream": "chat",
            "streams": {
                "s1": {
                    "channel": "stream.echo.s1",
                    "token": "tok1",
                    "direction": "outbound",
                    "format": "bytes",
                    "affinity": "dedicated",
                }
            },
        }
        stream_msg.timetoken = _TT_BASE + 100

        fetch_chain = MagicMock()
        mock_pn.fetch_messages.return_value = fetch_chain
        fetch_chain.channels.return_value = fetch_chain
        fetch_chain.maximum_per_channel.return_value = fetch_chain
        fetch_chain.start.return_value = fetch_chain

        result_obj = MagicMock()
        result_obj.result.channels = {"u.org.t1": [stream_msg]}
        fetch_chain.sync.return_value = result_obj

        streams, artifacts, hwm, _terminal = client._fetch_and_parse_history(
            mock_pn, "u.org.t1", "echo", "t1",
            {"subscribe_key": "sub-c-test", "publish_key": ""},
        )

        assert "s1" in streams
        assert streams["s1"].descriptor.declared_stream == "chat"

    def test_missing_declared_stream_defaults_to_none(self):
        """When declaredStream is absent from history event, descriptor has None."""
        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="free",
            base_url="http://api.test",
            auth_provider=StaticAuthProvider("jwt"),
        )

        mock_pn = MagicMock()

        stream_msg = MagicMock()
        stream_msg.message = {
            "type": "progress",
            "taskId": "t1",
            "streamEvent": "stream_started",
            "streams": {
                "s1": {
                    "channel": "stream.echo.s1",
                    "token": "tok1",
                    "direction": "outbound",
                    "format": "bytes",
                    "affinity": "dedicated",
                }
            },
        }
        stream_msg.timetoken = _TT_BASE + 100

        fetch_chain = MagicMock()
        mock_pn.fetch_messages.return_value = fetch_chain
        fetch_chain.channels.return_value = fetch_chain
        fetch_chain.maximum_per_channel.return_value = fetch_chain
        fetch_chain.start.return_value = fetch_chain

        result_obj = MagicMock()
        result_obj.result.channels = {"u.org.t1": [stream_msg]}
        fetch_chain.sync.return_value = result_obj

        streams, artifacts, hwm, _terminal = client._fetch_and_parse_history(
            mock_pn, "u.org.t1", "echo", "t1",
            {"subscribe_key": "sub-c-test", "publish_key": ""},
        )

        assert "s1" in streams
        assert streams["s1"].descriptor.declared_stream is None
