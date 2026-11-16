"""The small request/response types the web layer hands to the handlers."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any


@dataclass(frozen=True)
class Request:
    customer_id: str
    query: dict[str, str] = field(default_factory=dict)


@dataclass(frozen=True)
class Response:
    status: int
    body: Any
