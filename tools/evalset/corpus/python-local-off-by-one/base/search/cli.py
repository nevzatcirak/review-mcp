"""Maintenance commands for the search index."""

from __future__ import annotations

import argparse
import sys

from search.index import SearchIndex
from search.repository import ProductRepository


def stats(index: SearchIndex, repo: ProductRepository) -> str:
    return f"{len(index)} products indexed, {len(repo.all_ids())} in the catalog"


def main(argv: list[str], index: SearchIndex, repo: ProductRepository) -> int:
    parser = argparse.ArgumentParser(prog="search")
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("stats", help="compare the index with the catalog")
    args = parser.parse_args(argv)

    if args.command == "stats":
        print(stats(index, repo))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:], SearchIndex(), ProductRepository()))
