"""A per-task view of an agent's one shared ``TaskClient`` whose calls name the task they are made from inside.

The backend carries that task's user down to the task the call creates. A header, not a request field: the
backend's request schema is strict, so a backend without this ignores a header but would refuse a field.
Parity with Node ``parent-task-client.ts``.
"""

from typing import Dict, Optional

from .task_client import TaskClient

PARENT_TASK_HEADER = "Blocks-Parent-Task-Id"


class _ParentTaskClient(TaskClient):
    """Shares the shared client's state (its ``__dict__``) and overrides only the headers its calls send."""

    __slots__ = ("_blocks_parent_task_id",)

    def __init__(self, client: TaskClient, parent_task_id: str) -> None:  # noqa: D107 - shares state, no super()
        object.__setattr__(self, "__dict__", client.__dict__)
        object.__setattr__(self, "_blocks_parent_task_id", parent_task_id)

    @property
    def _rpc_headers(self) -> Optional[Dict[str, str]]:
        shared = self.__dict__.get("_rpc_headers") or {}
        return {**shared, PARENT_TASK_HEADER: self._blocks_parent_task_id}

    @_rpc_headers.setter
    def _rpc_headers(self, value: Optional[Dict[str, str]]) -> None:
        self.__dict__["_rpc_headers"] = value


def task_client_for_parent(client: TaskClient, parent_task_id: str) -> TaskClient:
    """The view the SDK hands a handler: calls through it name ``parent_task_id``."""
    return _ParentTaskClient(client, parent_task_id)
