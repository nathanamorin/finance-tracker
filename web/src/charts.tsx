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
import type { CashFlowRow, ValuePoint } from "./api";
import { money, moneyCompact, monthLabel, shortDate } from "./format";

const axisProps = {
  stroke: "var(--axis)",
  tick: { fill: "var(--muted)", fontSize: 12 },
  tickLine: false,
} as const;

interface TipProps {
  active?: boolean;
  label?: string;
  payload?: { name: string; value: number; color: string; dataKey: string }[];
  labelFormat: (l: string) => string;
}

function ChartTooltip({ active, payload, label, labelFormat }: TipProps) {
  if (!active || !payload?.length || !label) return null;
  return (
    <div className="chart-tooltip">
      <div className="t-date">{labelFormat(label)}</div>
      {payload.map((p) => (
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
    </div>
  );
}

/** Portfolio value vs. cumulative net contributions — both dollars, one axis. */
export function ValueChart({ points }: { points: ValuePoint[] }) {
  if (points.length < 2) return <div className="empty">Not enough history yet.</div>;
  return (
    <>
      <div className="legend" style={{ marginBottom: 8 }}>
        <span><span className="key" style={{ background: "var(--series-1)" }} />Portfolio value</span>
        <span style={{ color: "var(--series-2)" }}>
          <span className="key dashed" />
          <span style={{ color: "var(--text-2)" }}>Net contributions</span>
        </span>
      </div>
      <div style={{ width: "100%", height: 300 }}>
        <ResponsiveContainer>
          <ComposedChart data={points} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
            <defs>
              <linearGradient id="valueFill" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor="var(--series-1)" stopOpacity={0.18} />
                <stop offset="100%" stopColor="var(--series-1)" stopOpacity={0} />
              </linearGradient>
            </defs>
            <CartesianGrid stroke="var(--grid)" vertical={false} />
            <XAxis dataKey="date" {...axisProps} tickFormatter={monthLabel} minTickGap={40} />
            <YAxis {...axisProps} axisLine={false} tickFormatter={moneyCompact} width={64} />
            <Tooltip
              content={<ChartTooltip labelFormat={shortDate} />}
              cursor={{ stroke: "var(--axis)", strokeWidth: 1 }}
            />
            <Area
              type="monotone"
              dataKey="value"
              name="Portfolio value"
              stroke="var(--series-1)"
              strokeWidth={2}
              fill="url(#valueFill)"
              activeDot={{ r: 4, stroke: "var(--surface)", strokeWidth: 2 }}
              isAnimationActive={false}
            />
            <Line
              type="stepAfter"
              dataKey="net_contributions"
              name="Net contributions"
              stroke="var(--series-2)"
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

/** Investment income (dividends + interest + capital gains) per period. */
export function IncomeChart({ rows }: { rows: CashFlowRow[] }) {
  const data = rows
    .map((r) => ({ period: r.period, income: Math.round((r.dividends + r.interest + r.capital_gains) * 100) / 100 }))
    .slice(-24);
  if (!data.some((d) => d.income !== 0)) return <div className="empty">No income recorded yet.</div>;
  return (
    <div style={{ width: "100%", height: 220 }}>
      <ResponsiveContainer>
        <BarChart data={data} margin={{ top: 8, right: 8, bottom: 0, left: 0 }} barCategoryGap={2}>
          <CartesianGrid stroke="var(--grid)" vertical={false} />
          <XAxis dataKey="period" {...axisProps} tickFormatter={(p: string) => monthLabel(`${p}-01`)} minTickGap={24} />
          <YAxis {...axisProps} axisLine={false} tickFormatter={moneyCompact} width={56} />
          <Tooltip
            content={<ChartTooltip labelFormat={(p) => monthLabel(`${p}-01`)} />}
            cursor={{ fill: "var(--surface-2)" }}
          />
          <Bar dataKey="income" name="Income" fill="var(--series-1)" radius={[4, 4, 0, 0]} isAnimationActive={false} />
        </BarChart>
      </ResponsiveContainer>
    </div>
  );
}
