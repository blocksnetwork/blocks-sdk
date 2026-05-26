"""BLOCKS-456: regression fence on user-visible PubNub/PAM vocabulary.

Reads agent_instance.py source and asserts that no string literal in the
file contains the branded substrings that were de-branded by BLOCKS-456.
Internal identifiers (import statements, type references, parameter
names, comments) are NOT in scope and are not visible to ``ast.Constant``
string-literal walks.
"""

from __future__ import annotations

import ast
import pathlib

import pytest

_SOURCE = (
    pathlib.Path(__file__).resolve().parent.parent
    / "blocks_network"
    / "agent_instance.py"
)


def _string_literals(path: pathlib.Path) -> list[tuple[int, str]]:
    tree = ast.parse(path.read_text())
    out: list[tuple[int, str]] = []
    for node in ast.walk(tree):
        if isinstance(node, ast.Constant) and isinstance(node.value, str):
            out.append((node.lineno, node.value))
    return out


_BRANDED_SUBSTRINGS = (
    "pubnub_transport_",
    "pubnub transport",
    "PAM token expired",
    "PAM token applied",
    "filter expression on new PubNub",
    "filter expression on PubNub",
)


@pytest.mark.parametrize("branded", _BRANDED_SUBSTRINGS)
def test_no_branded_user_visible_strings(branded: str) -> None:
    offenders = [
        (lineno, lit)
        for lineno, lit in _string_literals(_SOURCE)
        if branded in lit
    ]
    assert not offenders, (
        f"Found branded substring {branded!r} in agent_instance.py at lines: "
        f"{[ln for ln, _ in offenders]}. After BLOCKS-456 these must be "
        f"de-branded."
    )
