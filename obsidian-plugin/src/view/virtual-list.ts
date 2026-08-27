export interface VirtualWindow<T> {
  start: number;
  items: T[];
  total: number;
}

export function virtualWindow<T>(items: T[], selectedIndex: number, maximum = 80, overscan = 20): VirtualWindow<T> {
  if (items.length <= maximum) return { start: 0, items, total: items.length };
  const anchor = Math.max(0, selectedIndex);
  const start = Math.max(0, Math.min(items.length - maximum, anchor - overscan));
  return { start, items: items.slice(start, start + maximum), total: items.length };
}
