export interface SankeySource { count: number }
export interface SankeyDest { count: number }
export const calls: unknown[] = [];
export function renderSankey(svg: unknown, empty: unknown, s: unknown, d: unknown, h?: number) {
  calls.push({ svg: (svg as { id?: string }).id, empty: (empty as { id?: string }).id, s, d, h });
}
