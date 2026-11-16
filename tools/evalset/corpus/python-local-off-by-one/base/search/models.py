from __future__ import annotations

from dataclasses import dataclass, field


@dataclass(frozen=True)
class Product:
    id: str
    title: str
    description: str = ""
    tags: tuple[str, ...] = field(default_factory=tuple)

    def terms(self) -> set[str]:
        words = f"{self.title} {self.description} {' '.join(self.tags)}".lower().split()
        return {word.strip(".,;:!?()") for word in words if word}
