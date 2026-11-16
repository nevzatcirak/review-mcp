"""Rebuilds the search index from the catalog."""

from __future__ import annotations

from search.index import SearchIndex
from search.repository import ProductRepository

DEFAULT_BATCH_SIZE = 500


def reindex_all(index: SearchIndex, repo: ProductRepository, batch_size: int = DEFAULT_BATCH_SIZE) -> int:
    """Clears the index and loads every product of the catalog into it, one
    batch at a time so that the replica is not asked for everything at once.
    Returns the number of products indexed."""
    if batch_size < 1:
        raise ValueError("batch_size must be at least 1")
    index.clear()
    ids = repo.all_ids()
    for start in range(0, len(ids) - 1, batch_size):
        index.update(repo.load(ids[start:start + batch_size]))
    return len(index)
