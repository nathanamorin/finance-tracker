import {
  Area,
  Bar,
  BarChart,
  CartesianGrid,
  ComposedChart,
  Line,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import type { AccountRef, IncomeRow, UpcomingVest, ValuePoint } from "./api";
import { money, moneyCompact, monthLabel, shortDate } from "./format";

const axisProps = {
  stroke: "var(--axis)",
  tick: { fill: "var(--muted)", fontSize: 12 },
  tickLine: false,
} as const;

/** Categorical slot colors (validated palette, fixed order, never cycled). */
const slot = (i: number) => `var(--series-${i + 1})`;
const OTHER_COLOR = "var(--muted)";

interface TipProps {
  active?: boolean;
  label?: string;
  payload?: { name: string; value: number; color: string; dataKey: string }[];
  labelFormat: (l: string) => string;
  total?: boolean;
}

function ChartTooltip({ active, payload, label, labelFormat, total }: TipProps) {
  if (!active || !payload?.length || !label) return null;
  const rows = payload.filter((p) => p.value !== 0 && p.value != null);
  const sum = rows.reduce((s, p) => s + p.value, 0);
  return (
    <div className="chart-tooltip">
      <div className="t-date">{labelFormat(label)}</div>
      {rows.map((p) => (
        <div className="t-row" key={p.dataKey}>
          <span>
            <span className="legend">
              <span className="key" style={{ background: p.color }} />
            </span>
            {p.name}
          </span>
          <strong>{money(p.value)}</strong>
        </div>
      ))}
      {total && rows.length > 1 && (
        <div className="t-row" style={{ borderTop: "1px solid var(--grid)", marginTop: 4, paddingTop: 4 }}>
          <span>Total</span>
          <strong>{money(sum)}</strong>
        </div>
      )}
    </div>
  );
}

/** Account slots: the total area takes slot 1, so accounts start at slot 2.
 *  Order is by account ID, so an account keeps its color across views. */
const MAX_ACCOUNT_LINES = 7;

export function accountColors(accounts: AccountRef[]) {
  const shown = accounts.slice(0, MAX_ACCOUNT_LINES);
  const folded = accounts.slice(MAX_ACCOUNT_LINES);
  return {
    lines: shown.map((a, i) => ({ key: `a${a.id}`, ids: [a.id], name: a.name, color: slot(i + 1) })),
    other: folded.length ? { key: "aother", ids: folded.map((a) => a.id), name: `Other accounts (${folded.length})`, color: OTHER_COLOR } : null,
  };
}

/** Portfolio value (total area) with a thin line per account, plus
 *  cumulative net contributions — all dollars, one axis. */
export function ValueChart({ points, accounts, byAccount }: { points: ValuePoint[]; accounts: AccountRef[]; byAccount: boolean }) {
  if (points.length < 2) return <div className="empty">Not enough history yet.</div>;
  const { lines, other } = accountColors(accounts);
  const series = byAccount && accounts.length > 1 ? [...lines, ...(other ? [other] : [])] : [];
  const data = points.map((p) => {
    const row: Record<string, number | string> = { date: p.date, value: p.value, net_contributions: p.net_contributions };
    for (const s of series) {
      row[s.key] = Math.round(s.ids.reduce((sum, id) => sum + (p.by_account?.[id] ?? 0), 0) * 100) / 100;
    }
    return row;
  });

  return (
    <>
      <div className="legend" style={{ marginBottom: 8 }}>
        <span><span className="key" style={{ background: "var(--series-1)" }} />Total</span>
        {series.map((s) => (
          <span key={s.key}><span className="key" style={{ background: s.color }} />{s.name}</span>
        ))}
        <span style={{ color: "var(--muted)" }}>
          <span className="key dashed" />
          <span style={{ color: "var(--text-2)" }}>Net contributions</span>
        </span>
      </div>
      <div style={{ width: "100%", height: 320 }}>
        <ResponsiveContainer>
          <ComposedChart data={data} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
            <defs>
              <linearGradient id="valueFill" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor="var(--series-1)" stopOpacity={0.18} />
                <stop offset="100%" stopColor="var(--series-1)" stopOpacity={0} />
              </linearGradient>
            </defs>
            <CartesianGrid stroke="var(--grid)" vertical={false} />
            <XAxis dataKey="date" {...axisProps} tickFormatter={monthLabel} minTickGap={40} />
            <YAxis {...axisProps} axisLine={false} tickFormatter={moneyCompact} width={64} />
            <Tooltip content={<ChartTooltip labelFormat={shortDate} />} cursor={{ stroke: "var(--axis)", strokeWidth: 1 }} />
            <Area
              type="linear"
              dataKey="value"
              name="Total"
              stroke="var(--series-1)"
              strokeWidth={2}
              fill="url(#valueFill)"
              activeDot={{ r: 4, stroke: "var(--surface)", strokeWidth: 2 }}
              isAnimationActive={false}
            />
            {series.map((s) => (
              <Line
                key={s.key}
                type="linear"
                dataKey={s.key}
                name={s.name}
                stroke={s.color}
                strokeWidth={1.5}
                dot={false}
                activeDot={{ r: 4, stroke: "var(--surface)", strokeWidth: 2 }}
                isAnimationActive={false}
              />
            ))}
            <Line
              type="stepAfter"
              dataKey="net_contributions"
              name="Net contributions"
              stroke="var(--muted)"
              strokeWidth={2}
              strokeDasharray="5 3"
              dot={false}
              activeDot={{ r: 4, stroke: "var(--surface)", strokeWidth: 2 }}
              isAnimationActive={false}
            />
          </ComposedChart>
        </ResponsiveContainer>
      </div>
    </>
  );
}

/** At most 7 named holdings + "Other" (8 categorical slots, never cycled). */
const MAX_INCOME_SERIES = 7;

/** Pivot income rows into stacked-bar data: top earners by total income
 *  over the shown periods get a slot each; the rest fold into "Other". */
export function pivotIncome(rows: IncomeRow[], lastN = 24) {
  const periods = [...new Set(rows.map((r) => r.period))].sort().slice(-lastN);
  const inRange = new Set(periods);
  const totals = new Map<string, { label: string; name: string; total: number }>();
  for (const r of rows) {
    if (!inRange.has(r.period)) continue;
    const t = totals.get(r.label) ?? { label: r.label, name: r.name, total: 0 };
    t.total += r.amount;
    totals.set(r.label, t);
  }
  const ranked = [...totals.values()].sort((a, b) => b.total - a.total);
  const needsOther = ranked.length > MAX_INCOME_SERIES + 1;
  const named = needsOther ? ranked.slice(0, MAX_INCOME_SERIES) : ranked;
  const namedSet = new Set(named.map((n) => n.label));
  const series = named.map((n, i) => ({ key: n.label, name: n.label, title: n.name, color: slot(i) }));
  if (needsOther) series.push({ key: "__other", name: `Other (${ranked.length - named.length})`, title: "Other holdings", color: OTHER_COLOR });

  const data = periods.map((p) => {
    const row: Record<string, number | string> = { period: p };
    for (const s of series) row[s.key] = 0;
    for (const r of rows) {
      if (r.period !== p) continue;
      const key = namedSet.has(r.label) ? r.label : "__other";
      row[key] = Math.round(((row[key] as number) + r.amount) * 100) / 100;
    }
    return row;
  });
  return { data, series };
}

/** Investment income per month, stacked by holding. */
export function IncomeChart({ rows }: { rows: IncomeRow[] }) {
  const { data, series } = pivotIncome(rows);
  if (data.length === 0) return <div className="empty">No income recorded yet.</div>;
  return (
    <>
      <div className="legend" style={{ marginBottom: 8 }}>
        {series.map((s) => (
          <span key={s.key} title={s.title}><span className="key swatch" style={{ background: s.color }} />{s.name}</span>
        ))}
      </div>
      <div style={{ width: "100%", height: 240 }}>
        <ResponsiveContainer>
          <BarChart data={data} margin={{ top: 8, right: 8, bottom: 0, left: 0 }} barCategoryGap={2}>
            <CartesianGrid stroke="var(--grid)" vertical={false} />
            <XAxis dataKey="period" {...axisProps} tickFormatter={(p: string) => monthLabel(`${p}-01`)} minTickGap={24} />
            <YAxis {...axisProps} axisLine={false} tickFormatter={moneyCompact} width={56} />
            <Tooltip
              content={<ChartTooltip labelFormat={(p) => monthLabel(`${p}-01`)} total />}
              cursor={{ fill: "var(--surface-2)" }}
            />
            {series.map((s, i) => (
              <Bar
                key={s.key}
                dataKey={s.key}
                name={s.name}
                stackId="income"
                fill={s.color}
                // 2px surface gap between stacked segments.
                stroke="var(--surface)"
                strokeWidth={1}
                radius={i === series.length - 1 ? [4, 4, 0, 0] : 0}
                isAnimationActive={false}
              />
            ))}
          </BarChart>
        </ResponsiveContainer>
      </div>
    </>
  );
}

/** Estimated value vesting on each upcoming vest date (pre-tax, at the latest price). */
export function VestChart({ upcoming }: { upcoming: UpcomingVest[] }) {
  const byDate = new Map<string, number>();
  for (const u of upcoming) byDate.set(u.vest_date, (byDate.get(u.vest_date) ?? 0) + u.est_value);
  const data = [...byDate.entries()].map(([date, value]) => ({ date, value: Math.round(value * 100) / 100 }));
  if (data.length === 0) return <div className="empty">No upcoming vests.</div>;
  return (
    <div style={{ width: "100%", height: 220 }}>
      <ResponsiveContainer>
        <BarChart data={data} margin={{ top: 8, right: 8, bottom: 0, left: 0 }} barCategoryGap={2}>
          <CartesianGrid stroke="var(--grid)" vertical={false} />
          <XAxis dataKey="date" {...axisProps} tickFormatter={shortDate} />
          <YAxis {...axisProps} axisLine={false} tickFormatter={moneyCompact} width={56} />
          <Tooltip content={<ChartTooltip labelFormat={shortDate} />} cursor={{ fill: "var(--surface-2)" }} />
          <Bar dataKey="value" name="Est. value vesting" fill="var(--series-1)" radius={[4, 4, 0, 0]} maxBarSize={56} isAnimationActive={false} />
        </BarChart>
      </ResponsiveContainer>
    </div>
  );
}
