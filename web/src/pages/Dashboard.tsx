import { useEffect, useState } from "react";
import { api, type CashFlowRow, type Summary, type ValueHistory } from "../api";
import { IncomeChart, ValueChart } from "../charts";
import { humanize, money, moneyWhole, percent, shortDate } from "../format";

const INTERVALS = ["week", "month"] as const;

export function Dashboard({ goImport, includeUnvested }: { goImport: () => void; includeUnvested: boolean }) {
  const [summary, setSummary] = useState<Summary>();
  const [history, setHistory] = useState<ValueHistory>();
  const [cash, setCash] = useState<CashFlowRow[]>([]);
  const [interval, setInterval] = useState<(typeof INTERVALS)[number]>("month");
  const [showTable, setShowTable] = useState(false);
  const [error, setError] = useState<string>();

  useEffect(() => {
    api.summary(includeUnvested).then(setSummary).catch((e) => setError(e.message));
    api.cashFlow("month").then(setCash).catch((e) => setError(e.message));
  }, []);
  useEffect(() => {
    api.valueHistory(interval, undefined, includeUnvested).then(setHistory).catch((e) => setError(e.message));
  }, [interval, includeUnvested]);

  if (error) return <p className="error">{error}</p>;
  if (!summary) return <p className="muted">Loading…</p>;
  if (summary.transaction_count === 0 && summary.total_value === 0 && summary.unvested_equity_value === 0) {
    return (
      <div className="card empty">
        <h2>No data yet</h2>
        <p>Upload a Vanguard download to get started.</p>
        <button className="btn primary" onClick={goImport}>Import data</button>
      </div>
    );
  }

  // Unvested awards are compensation not yet received, not investment growth.
  const owned = summary.total_value - (summary.includes_unvested ? summary.unvested_equity_value : 0);
  const growth = owned - summary.net_contributions_all_time;

  return (
    <>
      <div className="tiles">
        <div className="tile">
          <div className="label">Total value</div>
          <div className="value">{moneyWhole(summary.total_value)}</div>
          <div className="hint">
            {summary.includes_unvested && summary.unvested_equity_value > 0
              ? `incl. ${moneyWhole(summary.unvested_equity_value)} unvested`
              : summary.as_of ? `as of ${shortDate(summary.as_of)}` : "no positions imported"}
          </div>
        </div>
        <div className="tile">
          <div className="label">Net contributions (imported history)</div>
          <div className="value">{moneyWhole(summary.net_contributions_all_time)}</div>
          <div className="hint">{moneyWhole(summary.net_contributions_ytd)} this year</div>
        </div>
        <div className="tile">
          <div className="label">Value minus contributions</div>
          <div className="value">{moneyWhole(growth)}</div>
          <div className="hint">includes positions held before the imported date range</div>
        </div>
        {summary.unvested_equity_value > 0 && (
          <div className="tile">
            <div className="label">Unvested equity {summary.includes_unvested ? "(included in total)" : "(not in total)"}</div>
            <div className="value">{moneyWhole(summary.unvested_equity_value)}</div>
            <div className="hint">
              {summary.next_vest
                ? `next vest ${shortDate(summary.next_vest.vest_date)} · ${moneyWhole(summary.next_vest.est_value)}`
                : "no upcoming vests"}
            </div>
          </div>
        )}
        <div className="tile">
          <div className="label">Income, trailing 12 months</div>
          <div className="value">{moneyWhole(summary.income_ttm)}</div>
          <div className="hint">{money(summary.income_ytd)} this year</div>
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <h2>Value over time</h2>
            <div className="sub">Reconstructed from transactions, anchored to your latest positions snapshot. Accounts without transactions (e.g. 401(k) fund summaries) use their reported balances, starting from their first balance date.</div>
          </div>
          <div className="controls" style={{ margin: 0 }}>
            <div className="seg" role="group" aria-label="Interval">
              {INTERVALS.map((i) => (
                <button key={i} aria-pressed={interval === i} onClick={() => setInterval(i)}>{humanize(i)}</button>
              ))}
            </div>
            <button className="btn" onClick={() => setShowTable((s) => !s)}>{showTable ? "Chart" : "Table"}</button>
          </div>
        </div>
        {history && !showTable && <ValueChart points={history.points} />}
        {history && showTable && (
          <div className="table-wrap" style={{ maxHeight: 320 }}>
            <table>
              <thead><tr><th>Date</th><th className="num">Value</th><th className="num">Net contributions</th><th className="num">Cumulative income</th></tr></thead>
              <tbody>
                {[...history.points].reverse().map((p) => (
                  <tr key={p.date}><td>{shortDate(p.date)}</td><td className="num">{money(p.value)}</td><td className="num">{money(p.net_contributions)}</td><td className="num">{money(p.income)}</td></tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {history && history.unpriced_securities.length > 0 && (
          <div className="notice">No price is known for {history.unpriced_securities.join(", ")}, so those are left out of the history.</div>
        )}
      </div>

      <div className="grid-2">
        <div className="card">
          <div className="card-head"><h2>Monthly investment income</h2><span className="sub">dividends, interest, capital gains</span></div>
          <IncomeChart rows={cash} />
        </div>
        <div className="card">
          <div className="card-head"><h2>Allocation</h2><span className="sub">by asset class (or security type)</span></div>
          <div className="bars">
            {summary.allocation_by_class.map((s) => (
              <div className="bar-row" key={s.key}>
                <div className="bar-label"><span>{s.label.includes("_") || s.label === s.label.toLowerCase() ? humanize(s.label) : s.label.replace(/--/g, " · ")}</span><span>{percent(s.weight)} · {moneyWhole(s.value)}</span></div>
                <div className="bar-track"><div className="bar-fill" style={{ width: `${Math.max(s.weight * 100, 1)}%` }} /></div>
              </div>
            ))}
          </div>
          <div className="card-head" style={{ marginTop: 20 }}><h2>By account</h2></div>
          <div className="bars">
            {summary.allocation_by_account.map((s) => (
              <div className="bar-row" key={s.key}>
                <div className="bar-label"><span>{s.label}</span><span>{percent(s.weight)} · {moneyWhole(s.value)}</span></div>
                <div className="bar-track"><div className="bar-fill" style={{ width: `${Math.max(s.weight * 100, 1)}%` }} /></div>
              </div>
            ))}
          </div>
        </div>
      </div>
    </>
  );
}
