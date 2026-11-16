from __future__ import annotations

from collections.abc import Iterable

from search.models import Product


class ProductRepository:
    """The catalog as the search sees it; in production a read replica."""

    def __init__(self, products: Iterable[Product] = ()) -> None:
        self._products = {product.id: product for product in products}

    def all_ids(self) -> list[str]:
        return sorted(self._products)

    def load(self, ids: list[str]) -> list[Product]:
        return [self._products[product_id] for product_id in ids if product_id in self._products]
