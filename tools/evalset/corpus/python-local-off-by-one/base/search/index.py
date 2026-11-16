from __future__ import annotations

from collections import defaultdict

from search.models import Product


class SearchIndex:
    """An inverted index from terms to product ids."""

    def __init__(self) -> None:
        self._terms: dict[str, set[str]] = defaultdict(set)
        self._docs: dict[str, set[str]] = {}

    def __len__(self) -> int:
        return len(self._docs)

    def update(self, products: list[Product]) -> None:
        for product in products:
            self.remove(product.id)
            terms = product.terms()
            self._docs[product.id] = terms
            for term in terms:
                self._terms[term].add(product.id)

    def remove(self, product_id: str) -> None:
        for term in self._docs.pop(product_id, set()):
            self._terms[term].discard(product_id)

    def clear(self) -> None:
        self._terms.clear()
        self._docs.clear()

    def query(self, text: str) -> list[str]:
        words = [word for word in text.lower().split() if word]
        if not words:
            return []
        hits = set.intersection(*(self._terms.get(word, set()) for word in words))
        return sorted(hits)
