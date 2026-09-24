// The Tools page's ping cards: Score, ms, Min, Max and Loss, live.
//
// ── THE SCORE IS AN R-FACTOR, NOT A HOUSE FORMULA ───────────────────────────
//
// The ITU-T G.107 E-model's simplified form, as VoIP monitors use it: latency
// and jitter fold into one effective delay, which costs a little per
// millisecond up to 160 ms and much more past it, and each percent of loss
// costs 2.5. R tops out at 93.2 on a perfect link, and is scaled here so that
// link reads 100. Chosen by the operator over a weighted penalty with
// thresholds of our own, because a standard measure is one somebody can look
// up (decided 2026-09-19).
//
// JITTER is the mean absolute difference between consecutive replies' times,
// over the replies the run carries.

import { el, sparkPoints } from '../dom';
import type { PingResult } from '../gen/payloads';

/** Mean absolute difference between consecutive times; 0 with fewer than two. */
export function jitterOf(rtts: number[]): number {
  if (rtts.length < 2) return 0;
  let sum = 0;
  for (let i = 1; i < rtts.length; i++) sum += Math.abs(rtts[i]! - rtts[i - 1]!);
  return sum / (rtts.length - 1);
}

/** The link's score out of 100, from average latency, jitter and loss. */
export function pingScore(avgMs: number, jitterMs: number, lossPct: number): number {
  const eff = avgMs + 2 * jitterMs + 10;
  let r = eff < 160 ? 93.2 - eff / 40 : 93.2 - (eff - 120) / 10;
  r -= 2.5 * lossPct;
  return Math.max(0, Math.min(100, Math.round((r / 93.2) * 100)));
}

/** The score's grade: good, fair or poor, which the ring's colour follows. */
export function scoreGrade(score: number): 'good' | 'fair' | 'poor' {
  return score >= 80 ? 'good' : score >= 60 ? 'fair' : 'poor';
}

/** What the cards show for one frame of a run; null fields are a dash. */
export interface PingCardValues {
  score: number | null;
  lastMs: number | null;
  minMs: number | null;
  maxMs: number | null;
  lossPct: number | null;
  spark: number[];
}

/** The cards' values from a run so far. Nothing sent yet is all dashes; sent
 *  and nothing back scores 0. */
export function pingCardValues(r: PingResult): PingCardValues {
  const rtts = r.replies.filter((p) => p.status === '' && p.rttMs != null).map((p) => p.rttMs as number);
  const last = r.replies.length ? r.replies[r.replies.length - 1]! : null;
  let score: number | null = null;
  if (r.sent > 0) score = r.avgMs == null ? 0 : pingScore(r.avgMs, jitterOf(rtts), r.lossPct);
  return {
    score,
    lastMs: last && last.status === '' ? last.rttMs : null,
    minMs: r.minMs,
    maxMs: r.maxMs,
    lossPct: r.sent > 0 ? r.lossPct : null,
    spark: rtts.slice(-30),
  };
}

const RING_C = 2 * Math.PI * 34;

const reduced = (): boolean =>
  typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches;

function fmtMs(v: number): string {
  return v < 1 ? v.toFixed(2) : v < 100 ? v.toFixed(1) : String(Math.round(v));
}

/** Counts a number from what it shows to `to`, over a quarter second. Shared
 *  with the Security Scan's score. */
export function tween(node: HTMLElement | null, to: number | null, fmt: (v: number) => string): void {
  if (!node) return;
  if (to == null) {
    node.textContent = '-';
    delete node.dataset.v;
    return;
  }
  const from = node.dataset.v != null ? Number(node.dataset.v) : to;
  node.dataset.v = String(to);
  if (reduced() || from === to || typeof requestAnimationFrame !== 'function') {
    node.textContent = fmt(to);
    return;
  }
  const t0 = performance.now();
  const step = (now: number): void => {
    if (node.dataset.v !== String(to)) return; // a newer value took over
    const k = Math.min(1, (now - t0) / 250);
    node.textContent = fmt(from + (to - from) * (1 - Math.pow(1 - k, 3)));
    if (k < 1) requestAnimationFrame(step);
  };
  requestAnimationFrame(step);
}

/** Restarts a card's pulse, so each new reply is seen to land. */
function pulse(card: Element | null): void {
  if (!card || reduced() || typeof requestAnimationFrame !== 'function') return;
  card.classList.remove('tool-card-tick');
  requestAnimationFrame(() => requestAnimationFrame(() => card.classList.add('tool-card-tick')));
}

let lastSent = 0;

/** Draws the cards for a run so far; null clears them. */
export function renderPingCards(r: PingResult | null): void {
  const v: PingCardValues = r ? pingCardValues(r) :
    { score: null, lastMs: null, minMs: null, maxMs: null, lossPct: null, spark: [] };
  const ring = el('pingScoreRing');
  if (ring) {
    const pct = v.score == null ? 0 : v.score / 100;
    ring.setAttribute('stroke-dasharray', RING_C.toFixed(1));
    ring.setAttribute('stroke-dashoffset', (RING_C * (1 - pct)).toFixed(1));
  }
  const scoreCard = el('pingCardScore');
  if (scoreCard) {
    // Spelled out, not built from a prefix: a class assembled at run time is one
    // the stylesheet check (TestToggledClassesAreAnswered) cannot see.
    const g = v.score == null ? null : scoreGrade(v.score);
    scoreCard.classList.toggle('grade-good', g === 'good');
    scoreCard.classList.toggle('grade-fair', g === 'fair');
    scoreCard.classList.toggle('grade-poor', g === 'poor');
  }
  tween(el('pingScoreVal'), v.score, (x) => String(Math.round(x)));
  tween(el('pingLastVal'), v.lastMs, fmtMs);
  tween(el('pingMinVal'), v.minMs, fmtMs);
  tween(el('pingMaxVal'), v.maxMs, fmtMs);
  tween(el('pingLossVal'), v.lossPct, (x) => String(Math.round(x)));
  el('pingCardLoss')?.classList.toggle('is-bad', (v.lossPct ?? 0) > 0);
  const spark = el('pingSpark');
  if (spark) spark.setAttribute('points', v.spark.length > 1 ? sparkPoints(v.spark, 120, 28, 2) : '');
  const sent = r ? r.sent : 0;
  if (sent > lastSent) pulse(el('pingCardLast'));
  lastSent = sent;
}
