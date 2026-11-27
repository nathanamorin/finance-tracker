// Portfolio projection engine: pure functions, no React, unit-tested in
// projection.test.ts.
//
// Model (annual steps):
//   - Current holdings are grouped into asset-class buckets.
//   - Each year, every bucket grows by its return, then the year's
//     contribution is added, split by the "new money" allocation. Buckets
//     are not rebalanced: they drift, as an untouched portfolio would.
//   - "Expected" compounds each bucket at its expected annual return.
//   - Monte Carlo draws each bucket's yearly return from a normal
//     distribution with the given mean and volatility. A shared market
//     factor correlates the buckets (stocks move together; bonds a little;
//     cash not at all). Percentiles are taken across paths. Because of
//     volatility drag, the median path ends below the expected path.
//   - Real (today's-dollar) figures divide by (1 + inflation)^year.

import type { Holding } from "./api";

export type BucketId = "us_stocks" | "intl_stocks" | "bonds" | "cash" | "target_date" | "single_stock" | "other";

export interface BucketDef {
  id: BucketId;
  label: string;
  /** default expected annual return (fraction, nominal) */
  ret: number;
  /** default annual volatility (fraction) */
  vol: number;
  /** loading on the shared market factor, 0..1 */
  beta: number;
}

// Long-run nominal assumptions: deliberately round, middle-of-the-road
// placeholders for the user to replace with their own.
export const BUCKETS: BucketDef[] = [
  { id: "us_stocks", label: "US stocks", ret: 0.07, vol: 0.16, beta: 0.95 },
  { id: "intl_stocks", label: "International stocks", ret: 0.07, vol: 0.17, beta: 0.85 },
  { id: "bonds", label: "Bonds", ret: 0.04, vol: 0.06, beta: 0.2 },
  { id: "cash", label: "Cash & stable value", ret: 0.03, vol: 0.01, beta: 0 },
  { id: "target_date", label: "Target-date / balanced", ret: 0.06, vol: 0.11, beta: 0.9 },
  { id: "single_stock", label: "Individual & company stock", ret: 0.07, vol: 0.3, beta: 0.7 },
  { id: "other", label: "Other", ret: 0.05, vol: 0.1, beta: 0.5 },
];

export const bucketById = Object.fromEntries(BUCKETS.map((b) => [b.id, b])) as Record<BucketId, BucketDef>;

/** Stable key for a holding row, used for per-holding bucket overrides. */
export const holdingKey = (h: Pick<Holding, "account_id" | "security_id" | "unvested">) =>
  `${h.account_id}:${h.security_id}:${h.unvested ? "u" : "o"}`;

/** Best-guess bucket from the holding's type, asset class and name. */
export function classify(h: Pick<Holding, "security_type" | "asset_class" | "security_name" | "unvested">): BucketId {
  if (h.unvested || h.security_type === "stock") return "single_stock";
  const text = `${h.asset_class ?? ""} ${h.security_name}`.toLowerCase();
  if (h.security_type === "money_market" || h.security_type === "cash" || /stable value|money m|\bcash\b/.test(text)) return "cash";
  if (/target|lifecycle|lifepath|balanced|retirement 20\d\d|wellington|wellesley/.test(text)) return "target_date";
  if (/bond|treas|tax exempt|\bmuni?\b|fixed income|\btips\b|aggregate/.test(text)) return "bonds";
  if (/intl|international|ex-us|ex us|developed|emerging|foreign|world ex|pacific|europe/.test(text)) return "intl_stocks";
  if (/stock|equity|index|s&p|\b500\b|growth|value|russell|total market|small cap|mid cap|large cap/.test(text)) return "us_stocks";
  if (h.security_type === "mutual_fund" || h.security_type === "etf") return "us_stocks";
  return "other";
}

export interface BucketAssumption {
  ret: number;
  vol: number;
  /** share of new contributions, fraction; normalized when simulating */
  alloc: number;
}

export interface ProjectionInputs {
  /** starting value per bucket */
  start: Partial<Record<BucketId, number>>;
  years: number;
  annualContribution: number;
  /** yearly increase of the contribution, fraction */
  contributionGrowth: number;
  inflation: number;
  real: boolean;
  assumptions: Record<BucketId, BucketAssumption>;
  paths?: number;
  seed?: number;
}

export interface ProjectionYear {
  year: number; // 0 = today
  expected: number;
  byBucket: Record<BucketId, number>; // expected path, per bucket
  p10: number;
  p50: number;
  p90: number;
  contributed: number; // cumulative contributions (incl. starting value)
}

/** mulberry32: small, fast, seedable PRNG so results are stable between renders. */
export function rng(seed: number) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/** Standard normal via Box–Muller. */
export function normal(rand: () => number) {
  let u = 0;
  while (u === 0) u = rand();
  return Math.sqrt(-2 * Math.log(u)) * Math.cos(2 * Math.PI * rand());
}

function percentile(sorted: Float64Array, p: number) {
  if (sorted.length === 0) return 0;
  const idx = (sorted.length - 1) * p;
  const lo = Math.floor(idx);
  const hi = Math.ceil(idx);
  return sorted[lo] + (sorted[hi] - sorted[lo]) * (idx - lo);
}

/** Normalized new-money weights; falls back to an even split if all zero. */
export function contributionWeights(assumptions: Record<BucketId, BucketAssumption>) {
  const ids = BUCKETS.map((b) => b.id);
  const total = ids.reduce((s, id) => s + Math.max(0, assumptions[id].alloc), 0);
  return Object.fromEntries(ids.map((id) => [id, total > 0 ? Math.max(0, assumptions[id].alloc) / total : 1 / ids.length])) as Record<BucketId, number>;
}

export function project(inp: ProjectionInputs): ProjectionYear[] {
  const ids = BUCKETS.map((b) => b.id);
  const years = Math.max(0, Math.min(100, Math.round(inp.years)));
  const nPaths = inp.paths ?? 1000;
  const w = contributionWeights(inp.assumptions);
  const deflate = (t: number) => (inp.real ? Math.pow(1 + inp.inflation, t) : 1);
  const contribution = (t: number) => (t === 0 ? 0 : inp.annualContribution * Math.pow(1 + inp.contributionGrowth, t - 1));

  // Expected path.
  const exp: Record<BucketId, number> = Object.fromEntries(ids.map((id) => [id, inp.start[id] ?? 0])) as Record<BucketId, number>;
  const expectedByYear: Record<BucketId, number>[] = [{ ...exp }];
  for (let t = 1; t <= years; t++) {
    const c = contribution(t);
    for (const id of ids) exp[id] = exp[id] * (1 + inp.assumptions[id].ret) + c * w[id];
    expectedByYear.push({ ...exp });
  }

  // Monte Carlo totals per year.
  const totals: Float64Array[] = Array.from({ length: years + 1 }, () => new Float64Array(nPaths));
  const rand = rng(inp.seed ?? 42);
  const betas = ids.map((id) => bucketById[id].beta);
  const start = ids.map((id) => inp.start[id] ?? 0);
  const startTotal = start.reduce((a, b) => a + b, 0);
  const v = new Float64Array(ids.length);
  for (let p = 0; p < nPaths; p++) {
    for (let i = 0; i < ids.length; i++) v[i] = start[i];
    totals[0][p] = startTotal;
    for (let t = 1; t <= years; t++) {
      const z = normal(rand);
      const c = contribution(t);
      let total = 0;
      for (let i = 0; i < ids.length; i++) {
        const a = inp.assumptions[ids[i]];
        const b = betas[i];
        const shock = b * z + Math.sqrt(1 - b * b) * normal(rand);
        const r = Math.max(-0.95, a.ret + a.vol * shock);
        v[i] = v[i] * (1 + r) + c * w[ids[i]];
        total += v[i];
      }
      totals[t][p] = total;
    }
  }

  const out: ProjectionYear[] = [];
  let contributed = startTotal;
  for (let t = 0; t <= years; t++) {
    contributed += contribution(t);
    const sorted = totals[t].slice().sort();
    const d = deflate(t);
    const byBucket = Object.fromEntries(ids.map((id) => [id, expectedByYear[t][id] / d])) as Record<BucketId, number>;
    out.push({
      year: t,
      expected: ids.reduce((s, id) => s + expectedByYear[t][id], 0) / d,
      byBucket,
      p10: percentile(sorted, 0.1) / d,
      p50: percentile(sorted, 0.5) / d,
      p90: percentile(sorted, 0.9) / d,
      contributed: contributed / d,
    });
  }
  return out;
}
