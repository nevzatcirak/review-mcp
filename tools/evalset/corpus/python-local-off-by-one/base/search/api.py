from __future__ import annotations

from search.index import SearchIndex
from search.repository import ProductRepository


def search(index: SearchIndex, repo: ProductRepository, text: str, limit: int = 20) -> list[dict[str, str]]:
    ids = index.query(text)[:limit]
    return [{"id": product.id, "title": product.title} for product in repo.load(ids)]
