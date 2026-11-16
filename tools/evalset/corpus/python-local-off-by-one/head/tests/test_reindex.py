import pytest

from search.index import SearchIndex
from search.models import Product
from search.reindex import reindex_all
from search.repository import ProductRepository


def catalog(count: int) -> ProductRepository:
    return ProductRepository(Product(f"p-{n:04d}", f"Product {n}") for n in range(count))


def test_reindex_loads_the_whole_catalog_in_batches():
    index = SearchIndex()

    assert reindex_all(index, catalog(250), batch_size=100) == 250
    assert index.query("product") == [f"p-{n:04d}" for n in range(250)]


def test_reindex_drops_products_no_longer_in_the_catalog():
    index = SearchIndex()
    index.update([Product("gone", "Old product")])

    reindex_all(index, catalog(3))

    assert "gone" not in index.query("product")


def test_batch_size_must_be_positive():
    with pytest.raises(ValueError):
        reindex_all(SearchIndex(), catalog(1), batch_size=0)
