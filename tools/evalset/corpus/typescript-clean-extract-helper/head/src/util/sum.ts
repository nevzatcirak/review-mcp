/** Adds up value(item) over items, from left to right, starting at 0. */
export function sumBy<T>(items: readonly T[], value: (item: T) => number): number {
  return items.reduce((sum, item) => sum + value(item), 0);
}
