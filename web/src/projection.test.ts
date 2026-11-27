import { describe, expect, it } from "vitest";
import { BUCKETS, type BucketAssumption, type BucketId, classify, contributionWeights, project, rng } from "./projection";

const flat = (ret: number, vol = 0, alloc = 0): Record<BucketId, BucketAssumption> =>
  Object.fromEntries(BUCKETS.map((b) => [b.id, { ret, vol, alloc }])) as Record<BucketId, BucketAssumption>;

describe("classify", () => {
  // Security names/classes as they appear in the FAKE DATA test fixtures.
  const h = (security_name: string, security_type = "mutual_fund", asset_class: string | null = null, unvested = false) => ({
    security_name,
    security_type,
    asset_class,
    unvested,
  });
  it("maps fixture holdings to sensible buckets", () => {
    expect(classify(h("VANGUARD 500 INDEX ADMIRAL CL"))).toBe("us_stocks");
    expect(classify(h("VANGUARD MUN BOND TAX EXEMPT INDEX ADMIRAL CL"))).toBe("bonds");
    expect(classify(h("VANGUARD RUSSELL 1000 GROWTH INDEX FUND ETF", "etf"))).toBe("us_stocks");
    expect(classify(h("VANGUARD FEDERAL MONEY MARKET INVESTOR CL", "money_market"))).toBe("cash");
    expect(classify(h("U.S Stock Market - Index", "mutual_fund", "Stocks--US Stocks"))).toBe("us_stocks");
    expect(classify(h("International Index Fund", "mutual_fund", "Stocks--International Stocks"))).toBe("intl_stocks");
    expect(classify(h("Stable Value Fund", "mutual_fund", "Stable Value"))).toBe("cash");
    expect(classify(h("VANGUARD TARGET RETIREMENT 2055 FUND"))).toBe("target_date");
    expect(classify(h("XYZ", "stock", null, true))).toBe("single_stock");
  });
});

describe("project", () => {
  it("compounds the expected path exactly", () => {
    const res = project({
      start: { us_stocks: 1000 },
      years: 10,
      annualContribution: 0,
      contributionGrowth: 0,
      inflation: 0,
      real: false,
      assumptions: flat(0.07),
      paths: 10,
    });
    expect(res).toHaveLength(11);
    expect(res[10].expected).toBeCloseTo(1000 * 1.07 ** 10, 6);
    expect(res[10].byBucket.us_stocks).toBeCloseTo(1000 * 1.07 ** 10, 6);
  });

  it("adds contributions at year end, growing yearly, split by allocation", () => {
    const a = flat(0);
    a.bonds.alloc = 1;
    a.us_stocks.alloc = 3;
    const res = project({
      start: {},
      years: 2,
      annualContribution: 100,
      contributionGrowth: 0.1,
      inflation: 0,
      real: false,
      assumptions: a,
      paths: 5,
    });
    expect(res[1].expected).toBeCloseTo(100);
    expect(res[2].expected).toBeCloseTo(210);
    expect(res[2].byBucket.us_stocks).toBeCloseTo(157.5);
    expect(res[2].byBucket.bonds).toBeCloseTo(52.5);
    expect(res[2].contributed).toBeCloseTo(210);
  });

  it("with zero volatility, every Monte Carlo percentile equals the expected path", () => {
    const res = project({
      start: { us_stocks: 500, bonds: 500 },
      years: 5,
      annualContribution: 50,
      contributionGrowth: 0,
      inflation: 0,
      real: false,
      assumptions: flat(0.05, 0, 1),
      paths: 50,
    });
    for (const y of res) {
      expect(y.p10).toBeCloseTo(y.expected, 6);
      expect(y.p90).toBeCloseTo(y.expected, 6);
    }
  });

  it("orders percentiles, and shows volatility drag (median below expected)", () => {
    const res = project({
      start: { single_stock: 10000 },
      years: 30,
      annualContribution: 0,
      contributionGrowth: 0,
      inflation: 0,
      real: false,
      assumptions: flat(0.07, 0.3),
      paths: 2000,
    });
    const end = res[30];
    expect(end.p10).toBeLessThan(end.p50);
    expect(end.p50).toBeLessThan(end.p90);
    expect(end.p50).toBeLessThan(end.expected);
  });

  it("deflates to today's dollars", () => {
    const base = { start: { cash: 1000 }, years: 10, annualContribution: 0, contributionGrowth: 0, inflation: 0.03, assumptions: flat(0.03), paths: 5 };
    expect(project({ ...base, real: true })[10].expected).toBeCloseTo(1000, 6);
    expect(project({ ...base, real: false })[10].expected).toBeCloseTo(1000 * 1.03 ** 10, 6);
  });

  it("is deterministic for a given seed", () => {
    const inp = { start: { us_stocks: 1000 }, years: 10, annualContribution: 0, contributionGrowth: 0, inflation: 0, real: false, assumptions: flat(0.07, 0.16), paths: 200, seed: 7 };
    expect(project(inp)[10].p50).toBe(project(inp)[10].p50);
  });
});

describe("helpers", () => {
  it("normalizes contribution weights and falls back to an even split", () => {
    const w = contributionWeights(flat(0, 0, 0));
    expect(Object.values(w).reduce((a, b) => a + b, 0)).toBeCloseTo(1);
    expect(w.us_stocks).toBeCloseTo(1 / BUCKETS.length);
  });
  it("rng yields values in [0,1)", () => {
    const r = rng(1);
    for (let i = 0; i < 1000; i++) {
      const x = r();
      expect(x).toBeGreaterThanOrEqual(0);
      expect(x).toBeLessThan(1);
    }
  });
});
