"""Per-publisher ``seq`` bookkeeping. Not thread-safe: the owning receiver holds the lock."""

from __future__ import annotations

import bisect
import enum
import math
from dataclasses import dataclass, field
from typing import Any, Callable, Dict, Generic, List, Optional, Set, Tuple, TypeVar

from . import wire

T = TypeVar("T")


def is_sender_seq(seq: Any) -> bool:
    return isinstance(seq, int) and not isinstance(seq, bool) and 1 <= seq <= wire.MAX_SEQ


@dataclass
class _HeldSender(Generic[T]):
    next_seq: int = 1
    held: Dict[int, T] = field(default_factory=dict)
    # Held seqs history also returned, so a gap below one was missing from history too.
    replayed: Set[int] = field(default_factory=set)


class InOrderBuffer(Generic[T]):
    """Releases each publisher's items to ``deliver`` in ``seq`` order, one at a time, holding
    any that arrive ahead of a gap.

    A ``seq`` below the next expected one, or one already held, is a duplicate. A ``seq``
    skipped past by :meth:`skip_gaps` counts as a duplicate if it turns up later, since
    releasing it then would break order. An item leaves the held set just before it is
    delivered, so ``deliver`` sees only what is still waiting.
    """

    def __init__(self, deliver: Callable[[T], None]) -> None:
        self._deliver = deliver
        self._senders: Dict[str, _HeldSender[T]] = {}
        self._disposed = False

    def accept(self, publisher: str, seq: int, item: T, replayed: bool = False) -> bool:
        """False for a duplicate, or once disposed. ``replayed`` marks an item read from history."""
        if self._disposed:
            return False
        sender = self._senders.setdefault(publisher, _HeldSender())
        if seq < sender.next_seq:
            return False
        if seq in sender.held:
            if replayed:
                sender.replayed.add(seq)
            return False
        sender.held[seq] = item
        if replayed:
            sender.replayed.add(seq)
        self._drain(sender)
        return True

    @property
    def holding(self) -> bool:
        return any(s.held for s in self._senders.values())

    def has_gap(self, publisher: str) -> bool:
        sender = self._senders.get(publisher)
        return bool(sender and sender.held)

    def highest_held(self, publisher: str) -> Optional[int]:
        sender = self._senders.get(publisher)
        return max(sender.held) if sender and sender.held else None

    def held_cutoffs(self) -> Dict[str, int]:
        """Each publisher's highest held ``seq``: the gaps a recovery is asked to close."""
        return {publisher: max(s.held) for publisher, s in self._senders.items() if s.held}

    def replayed_cutoffs(self) -> Dict[str, int]:
        """Each publisher's highest held ``seq`` history returned: the gaps a replay revealed."""
        return {p: max(s.replayed) for p, s in self._senders.items() if s.replayed}

    def held_items(self) -> List[T]:
        """Every item held behind a gap, each publisher's in ``seq`` order."""
        return [s.held[seq] for s in self._senders.values() for seq in sorted(s.held)]

    def holds_any(self, matches: Callable[[T], bool]) -> bool:
        return any(matches(item) for s in self._senders.values() for item in s.held.values())

    def skip_gaps(self, publisher: str, through: int) -> bool:
        """Give up on every missing ``seq`` below a held ``seq`` at or under ``through``.

        Release what that frees, in order; True when anything was released. A gap above
        ``through`` is newer than the caller's window, so it stays open.
        """
        sender = self._senders.get(publisher)
        if sender is None:
            return False
        released = False
        for seq in sorted(s for s in sender.held if s <= through):
            if self._disposed:
                return released
            if seq in sender.held:
                self._release(sender, seq)
                released = True
        self._drain(sender)
        return released

    def dispose(self) -> None:
        self._disposed = True
        self._senders.clear()

    def _drain(self, sender: _HeldSender[T]) -> None:
        while not self._disposed and sender.next_seq in sender.held:
            self._release(sender, sender.next_seq)

    def _release(self, sender: _HeldSender[T], seq: int) -> None:
        item = sender.held.pop(seq)
        sender.replayed.discard(seq)
        sender.next_seq = seq + 1
        self._deliver(item)


class SeqVerdict(enum.Enum):
    DUPLICATE = "duplicate"
    IN_ORDER = "in_order"
    AFTER_GAP = "after_gap"


@dataclass
class _SeenSender:
    highest: int = 0
    # Inclusive (low, high) ranges, so a large seq jump costs one entry, not one per seq.
    missing: List[Tuple[int, int]] = field(default_factory=list)

    def fill(self, seq: int) -> bool:
        # Ranges are disjoint and appended in ascending order, so the only candidate is
        # the last one starting at or below ``seq``.
        i = bisect.bisect_right(self.missing, (seq, math.inf)) - 1
        if i < 0:
            return False
        low, high = self.missing[i]
        if seq > high:
            return False
        pieces = [(lo, hi) for lo, hi in ((low, seq - 1), (seq + 1, high)) if lo <= hi]
        self.missing[i : i + 1] = pieces
        return True


class SeqTracker:
    """Dedups each publisher's ``seq`` without holding anything back.

    Remembers only the highest ``seq`` seen and the ranges skipped below it, so a late
    arrival that fills a gap is still new.
    """

    def __init__(self) -> None:
        self._senders: Dict[str, _SeenSender] = {}

    def observe(self, publisher: str, seq: int) -> SeqVerdict:
        sender = self._senders.setdefault(publisher, _SeenSender())
        if seq > sender.highest:
            gapped = seq > sender.highest + 1
            if gapped:
                sender.missing.append((sender.highest + 1, seq - 1))
            sender.highest = seq
            return SeqVerdict.AFTER_GAP if gapped else SeqVerdict.IN_ORDER
        return SeqVerdict.IN_ORDER if sender.fill(seq) else SeqVerdict.DUPLICATE
