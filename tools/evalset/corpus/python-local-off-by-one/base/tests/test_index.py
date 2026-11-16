from search.index import SearchIndex
from search.models import Product


def test_query_needs_every_word():
    index = SearchIndex()
    index.update([
        Product("p-1", "Blue mug", "Stoneware, 300 ml"),
        Product("p-2", "Blue plate"),
        Product("p-3", "Red mug"),
    ])

    assert index.query("blue mug") == ["p-1"]
    assert index.query("mug") == ["p-1", "p-3"]


def test_update_replaces_old_terms():
    index = SearchIndex()
    index.update([Product("p-1", "Blue mug")])
    index.update([Product("p-1", "Green mug")])

    assert index.query("blue") == []
    assert index.query("green") == ["p-1"]
