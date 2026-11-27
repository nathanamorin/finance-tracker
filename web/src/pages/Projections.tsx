import { useEffect, useMemo, useRef, useState } from "react";
import { api, type CashFlowRow, type Holding } from "../api";
import { BucketStackChart, ProjectionChart, bucketColor } from "../charts";
import { money, moneyCompact, moneyWhole, percent, shares } from "../format";
import {
  BUCKETS,
  type BucketAssumption,
  type BucketId,
  bucketById,
  classify,
  contributionWeights,
  holdingKey,
  project,
} from "../projection";

/** Saved per user via /api/settings/projections (in their encrypted DB). */
interface Settings {
  version: 1;
  years: number;
  /** null = use the last 12 months of net contributions */
  annualContribution: number | null;
  contributionGrowth: number;
  inflation: number;
  real: boolean;
  /** only buckets the user has edited; others use defaults */
  assumptions: Partial<Record<BucketId, Partial<BucketAssumption>>>;
  /** holdingKey → bucket */
  overrides: Record<string, BucketId>;
}

const DEFAULTS: Settings = {
  version: 1,
  years: 30,
  annualContribution: null,
  contributionGrowth: 0,
  inflation: 0.025,
  real: false,
  assumptions: {},
  overrides: {},
};

const pct = (v: number) => Math.round(v * 10000) / 100; // fraction → percent number
const frac = (v: string) => (v === "" || Number.isNaN(Number(v)) ? 0 : Number(v) / 100);
const MILESTONES = [1, 5, 10, 15, 20, 25, 30, 40, 50];

export function Projections({ includeUnvested }: { includeUnvested: boolean }) {
  const [holdings, setHoldings] = useState<Holding[]>();
  const [cash, setCash] = useState<CashFlowRow[]>([]);
  const [settings, setSettings] = useState<Settings>(DEFAULTS);
  const [loaded, setLoaded] = useState(false);
  const [saveState, setSaveState] = useState<"" | "saving" | "saved" | "error">("");
  const [view, setView] = useState<"chart" | "table">("chart");
  const [error, setError] = useState<string>();
  const dirty = useRef(false);

  useEffect(() => {
    api.holdings(includeUnvested).then(setHoldings).catch((e) => setError(e.message));
    api.cashFlow("month").then(setCash).catch(() => {});
    api
      .getSetting<Settings>("projections")
      .then((saved) => {
        if (saved && saved.version === 1) setSettings({ ...DEFAULTS, ...saved });
      })
      .catch(() => {})
      .finally(() => setLoaded(true));
  }, [includeUnvested]);

  // Debounced save after user edits.
  useEffect(() => {
    if (!loaded || !dirty.current) return;
    setSaveState("saving");
    const t = setTimeout(() => {
      api
        .putSetting("projections", settings)
        .then(() => setSaveState("saved"))
        .catch(() => setSaveState("error"));
    }, 800);
    return () => clearTimeout(t);
  }, [settings, loaded]);

  const update = (patch: Partial<Settings>) => {
    dirty.current = true;
    setSettings((s) => ({ ...s, ...patch }));
  };

  // Current value per bucket.
  const rows = useMemo(
    () =>
      (holdings ?? [])
        .filter((h) => h.market_value > 0)
        .map((h) => {
          const auto = classify(h);
          const bucket = settings.overrides[holdingKey(h)] ?? auto;
          return { h, auto, bucket };
        }),
    [holdings, settings.overrides],
  );
  const start = useMemo(() => {
    const s: Partial<Record<BucketId, number>> = {};
    for (const r of rows) s[r.bucket] = (s[r.bucket] ?? 0) + r.h.market_value;
    return s;
  }, [rows]);
  const startTotal = Object.values(start).reduce((a, b) => a + (b ?? 0), 0);

  // Default new-money split: current weights, excluding individual/company
  // stock (which typically grows by vesting, not contributions).
  const defaultAlloc = useMemo(() => {
    const base = BUCKETS.filter((b) => b.id !== "single_stock");
    const tot = base.reduce((s, b) => s + (start[b.id] ?? 0), 0);
    return Object.fromEntries(BUCKETS.map((b) => [b.id, b.id === "single_stock" || tot === 0 ? 0 : (start[b.id] ?? 0) / tot])) as Record<BucketId, number>;
  }, [start]);

  const assumptions = useMemo(
    () =>
      Object.fromEntries(
        BUCKETS.map((b) => {
          const o = settings.assumptions[b.id] ?? {};
          return [b.id, { ret: o.ret ?? b.ret, vol: o.vol ?? b.vol, alloc: o.alloc ?? defaultAlloc[b.id] }];
        }),
      ) as Record<BucketId, BucketAssumption>,
    [settings.assumptions, defaultAlloc],
  );
  const weights = contributionWeights(assumptions);

  const autoContribution = useMemo(() => {
    const last12 = cash.slice(-12);
    return Math.max(0, Math.round(last12.reduce((s, r) => s + r.contributions - r.withdrawals, 0)));
  }, [cash]);
  const annualContribution = settings.annualContribution ?? autoContribution;

  const years = useMemo(
    () =>
      project({
        start,
        years: settings.years,
        annualContribution,
        contributionGrowth: settings.contributionGrowth,
        inflation: settings.inflation,
        real: settings.real,
        assumptions,
        paths: 1000,
      }),
    [start, settings.years, annualContribution, settings.contributionGrowth, settings.inflation, settings.real, assumptions],
  );

  const setAssumption = (id: BucketId, field: keyof BucketAssumption, value: number) =>
    update({ assumptions: { ...settings.assumptions, [id]: { ...settings.assumptions[id], [field]: value } } });

  if (error) return <p className="error">{error}</p>;
  if (!holdings || !loaded) return <p className="muted">Loading…</p>;
  if (startTotal === 0 && annualContribution === 0) {
    return <div className="card empty">Nothing to project yet. Import positions first.</div>;
  }

  const end = years[years.length - 1];
  const startYear = new Date().getFullYear();
  const usedBuckets = BUCKETS.map((b) => b.id).filter((id) => years.some((y) => y.byBucket[id] > 0.5));
  const dollars = settings.real ? "today's dollars" : "nominal dollars";
  const expectedBlend = BUCKETS.reduce((s, b) => s + ((start[b.id] ?? 0) / (startTotal || 1)) * assumptions[b.id].ret, 0);

  return (
    <>
      <div className="card">
        <div className="card-head">
          <h2>Projection settings</h2>
          <span className="sub">
            {saveState === "saving" ? "Saving…" : saveState === "saved" ? "Saved to your account" : saveState === "error" ? "Couldn't save settings" : "Settings are saved to your account"}
            {" · "}
            <button className="link" onClick={() => { dirty.current = true; setSettings({ ...DEFAULTS }); }}>Reset to defaults</button>
          </span>
        </div>
        <div className="form-grid">
          <label>
            <span>Timeframe: <strong>{settings.years} years</strong> (to {startYear + settings.years})</span>
            <input type="range" min={1} max={50} value={settings.years} onChange={(e) => update({ years: Number(e.target.value) })} />
          </label>
          <label>
            <span>Annual contribution {settings.annualContribution == null && <span className="muted">(last 12 months)</span>}</span>
            <div className="row">
              <input
                type="number"
                min={0}
                step={500}
                value={annualContribution}
                onChange={(e) => update({ annualContribution: Math.max(0, Number(e.target.value) || 0) })}
              />
              {settings.annualContribution != null && (
                <button className="link" onClick={() => update({ annualContribution: null })}>Use last 12 months ({moneyWhole(autoContribution)})</button>
              )}
            </div>
          </label>
          <label>
            <span>Contribution increase per year (%)</span>
            <input type="number" step={0.5} value={pct(settings.contributionGrowth)} onChange={(e) => update({ contributionGrowth: frac(e.target.value) })} />
          </label>
          <label>
            <span>Inflation (%)</span>
            <input type="number" step={0.1} value={pct(settings.inflation)} onChange={(e) => update({ inflation: frac(e.target.value) })} />
          </label>
          <label className="toggle" style={{ alignSelf: "end", paddingBottom: 8 }}>
            <input type="checkbox" checked={settings.real} onChange={(e) => update({ real: e.target.checked })} />
            Show in today's dollars (inflation-adjusted)
          </label>
        </div>

        <div className="table-wrap" style={{ marginTop: 16 }}>
          <table>
            <thead>
              <tr>
                <th>Asset class</th>
                <th className="num">Current value</th>
                <th className="num">Weight</th>
                <th className="num">Expected return %/yr</th>
                <th className="num">Volatility %/yr</th>
                <th className="num">New money %</th>
              </tr>
            </thead>
            <tbody>
              {BUCKETS.map((b) => (
                <tr key={b.id}>
                  <td><span className="inline-swatch" style={{ background: bucketColor(b.id) }} />{b.label}</td>
                  <td className="num">{money(start[b.id] ?? 0)}</td>
                  <td className="num">{startTotal ? percent((start[b.id] ?? 0) / startTotal) : "—"}</td>
                  <td className="num">
                    <input className="num-input" type="number" step={0.25} value={pct(assumptions[b.id].ret)} onChange={(e) => setAssumption(b.id, "ret", frac(e.target.value))} aria-label={`${b.label} expected return`} />
                  </td>
                  <td className="num">
                    <input className="num-input" type="number" step={0.5} min={0} value={pct(assumptions[b.id].vol)} onChange={(e) => setAssumption(b.id, "vol", Math.max(0, frac(e.target.value)))} aria-label={`${b.label} volatility`} />
                  </td>
                  <td className="num">
                    <input className="num-input" type="number" step={1} min={0} value={pct(assumptions[b.id].alloc)} onChange={(e) => setAssumption(b.id, "alloc", Math.max(0, frac(e.target.value)))} aria-label={`${b.label} share of new money`} />
                    <div className="muted" style={{ fontSize: "0.75rem" }}>{percent(weights[b.id])} effective</div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <p className="sub" style={{ marginBottom: 0 }}>
          Defaults are round long-run placeholders, not forecasts. Replace them with your own. Current blended expected return: {percent(expectedBlend)}/yr.
          New-money percentages are normalized to 100%.
        </p>
      </div>

      <div className="tiles">
        <div className="tile">
          <div className="label">At expected rates, {startYear + settings.years}</div>
          <div className="value">{moneyWhole(end.expected)}</div>
          <div className="hint">{dollars}</div>
        </div>
        <div className="tile">
          <div className="label">Median outcome</div>
          <div className="value">{moneyWhole(end.p50)}</div>
          <div className="hint">half of simulated paths end above this</div>
        </div>
        <div className="tile">
          <div className="label">10th–90th percentile</div>
          <div className="value" style={{ fontSize: "1.25rem", paddingTop: 6 }}>{moneyWhole(end.p10)} – {moneyWhole(end.p90)}</div>
          <div className="hint">80% of 1,000 simulated paths</div>
        </div>
        <div className="tile">
          <div className="label">Total contributed</div>
          <div className="value">{moneyWhole(end.contributed)}</div>
          <div className="hint">today's balance plus all future contributions</div>
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <h2>Projected value</h2>
            <div className="sub">
              Monte Carlo spread of outcomes in {dollars}, with the path at your expected rates. The median sits below that path because volatility drags down compounded growth.
            </div>
          </div>
          <button className="btn" onClick={() => setView((v) => (v === "chart" ? "table" : "chart"))}>{view === "chart" ? "Table" : "Chart"}</button>
        </div>
        {view === "chart" ? (
          <ProjectionChart years={years} startYear={startYear} />
        ) : (
          <div className="table-wrap" style={{ maxHeight: 360 }}>
            <table>
              <thead>
                <tr><th>Year</th><th className="num">Expected rates</th><th className="num">10th pct</th><th className="num">Median</th><th className="num">90th pct</th><th className="num">Contributed</th></tr>
              </thead>
              <tbody>
                {years.map((y) => (
                  <tr key={y.year}>
                    <td>{y.year === 0 ? "Today" : startYear + y.year}</td>
                    <td className="num">{money(y.expected)}</td><td className="num">{money(y.p10)}</td>
                    <td className="num">{money(y.p50)}</td><td className="num">{money(y.p90)}</td>
                    <td className="num">{money(y.contributed)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <div className="grid-2">
        <div className="card">
          <div className="card-head"><h2>Expected path by asset class</h2><span className="sub">no rebalancing: classes drift with their returns</span></div>
          <BucketStackChart years={years} startYear={startYear} buckets={usedBuckets} />
        </div>
        <div className="card">
          <div className="card-head"><h2>Milestones</h2><span className="sub">{dollars}</span></div>
          <div className="table-wrap">
            <table>
              <thead><tr><th>In</th><th className="num">Expected</th><th className="num">Median</th><th className="num">10th–90th</th></tr></thead>
              <tbody>
                {MILESTONES.filter((m) => m <= settings.years).concat(MILESTONES.includes(settings.years) ? [] : [settings.years]).map((m) => {
                  const y = years[m];
                  return (
                    <tr key={m}>
                      <td>{m} yr<div className="muted" style={{ fontSize: "0.75rem" }}>{startYear + m}</div></td>
                      <td className="num">{moneyCompact(y.expected)}</td>
                      <td className="num">{moneyCompact(y.p50)}</td>
                      <td className="num muted" style={{ whiteSpace: "normal" }}>{moneyCompact(y.p10)} – {moneyCompact(y.p90)}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </div>
      </div>

      <details className="card">
        <summary><strong>Holdings by asset class</strong> <span className="sub">· {rows.length} holdings · change any holding's class</span></summary>
        <div className="table-wrap" style={{ marginTop: 12 }}>
          <table>
            <thead><tr><th>Holding</th><th>Account</th><th className="num">Shares</th><th className="num">Value</th><th>Asset class</th></tr></thead>
            <tbody>
              {rows.map(({ h, auto, bucket }) => (
                <tr key={holdingKey(h)} className={h.unvested ? "unvested" : undefined}>
                  <td className="wrap"><strong>{h.symbol ?? ""}</strong> {h.security_name}{h.unvested && <span className="pill unvested" style={{ marginLeft: 6 }}>Unvested</span>}</td>
                  <td>{h.account_name}</td>
                  <td className="num">{shares(h.quantity)}</td>
                  <td className="num">{money(h.market_value)}</td>
                  <td>
                    <select
                      value={bucket}
                      onChange={(e) => {
                        const next = { ...settings.overrides };
                        if (e.target.value === auto) delete next[holdingKey(h)];
                        else next[holdingKey(h)] = e.target.value as BucketId;
                        update({ overrides: next });
                      }}
                      aria-label={`Asset class for ${h.security_name}`}
                    >
                      {BUCKETS.map((b) => (
                        <option key={b.id} value={b.id}>{b.label}{b.id === auto ? " (auto)" : ""}</option>
                      ))}
                    </select>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </details>

      <p className="sub">
        Assumptions: annual steps. Each year a class grows by its return, then that year's contribution is added. Random yearly returns are normally
        distributed and correlated through a shared market factor ({BUCKETS.map((b) => `${b.label.split(" ")[0]} ${bucketById[b.id].beta}`).join(", ")}).
        Taxes, fees and withdrawals aren't modeled. This is an exploration tool, not financial advice.
      </p>
    </>
  );
}
