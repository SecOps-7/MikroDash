// EVERY DATE AND TIME THE APP SHOWS, IN THE INSTALL'S DISPLAY TIMEZONE.
//
// `displayTimezone` (Settings) names the zone; empty means the browser's own.
// It was honoured by the top-bar clock alone. Reports and Audit kept their own
// copy of the zone, fed by a setter nothing called, and backups, account
// sessions, schedule runs, the database card and topology formatted in the
// browser's zone and locale, each its own way (review loop, decided 2026-09-19:
// fix it everywhere). One zone, read from caps.ts, and one format family:
// `YYYY-MM-DD HH:MM:SS`, trimmed where a column needs less.

import { getDisplayTimezone } from './caps';

/** Epoch milliseconds, or an ISO string as some payloads carry; missing is a dash. */
type Stamp = number | string | null | undefined;

interface Parts { Y: string; M: string; D: string; h: string; m: string; s: string }

const p2 = (n: number): string => String(n).padStart(2, '0');

/** The civil parts of `ts` in `tz`, or in the browser's zone when `tz` is empty. */
function parts(ts: Stamp, tz: string): Parts {
  const d = new Date(ts as number | string);
  if (!tz) {
    return { Y: String(d.getFullYear()), M: p2(d.getMonth() + 1), D: p2(d.getDate()),
      h: p2(d.getHours()), m: p2(d.getMinutes()), s: p2(d.getSeconds()) };
  }
  const got: Record<string, string> = {};
  // `hourCycle: 'h23'`, not `hour12: false`: the latter prints midnight as 24
  // in some engines.
  for (const x of new Intl.DateTimeFormat('en-GB', {
    timeZone: tz, year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
  }).formatToParts(d)) got[x.type] = x.value;
  return { Y: got.year || '', M: got.month || '', D: got.day || '',
    h: got.hour || '', m: got.minute || '', s: got.second || '' };
}

/** A timestamp for a table cell: `YYYY-MM-DD HH:MM:SS`, or without the seconds.
 *  An em dash for a missing one, never "1970". */
export function fmtTs(ts: Stamp, seconds = true, tz = getDisplayTimezone()): string {
  if (!ts) return '—';
  const x = parts(ts, tz);
  return x.Y + '-' + x.M + '-' + x.D + ' ' + x.h + ':' + x.m + (seconds ? ':' + x.s : '');
}

/** The date alone: `YYYY-MM-DD`. */
export function fmtDate(ts: Stamp, tz = getDisplayTimezone()): string {
  if (!ts) return '—';
  const x = parts(ts, tz);
  return x.Y + '-' + x.M + '-' + x.D;
}

/** The time alone: `HH:MM:SS`, or `HH:MM`. */
export function fmtTime(ts: Stamp, seconds = true, tz = getDisplayTimezone()): string {
  const x = parts(ts, tz);
  return x.h + ':' + x.m + (seconds ? ':' + x.s : '');
}

/** `MM-DD`, for a chart axis spanning days. */
export function fmtMonthDay(ts: Stamp, tz = getDisplayTimezone()): string {
  const x = parts(ts, tz);
  return x.M + '-' + x.D;
}
