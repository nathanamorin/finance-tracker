import { useEffect, useState } from "react";
import { api, type Holding, type Performance } from "../api";
import { humanize, money, percent, shares, shortDate } from "../format";

const signed = (v: number) => (v > 0 ? "+" : "") + money(v);

export function Holdings({ includeUnvested }: { includeUnvested: boolean }) {
  const [rows, setRows] = useState<Holding[]>();
  const [perf, setPerf] = useState<Performance[]>([]);
  const [error, setError] = useState<string>();
  useEffect(() => {
    // Unvested rows are always shown; the toggle decides whether they count.
    api.holdings(true).then(setRows).catch((e) => setError(e.message));
    api.performance().then(setPerf).catch((e) => setError(e.message));
  }, []);

  if (error) return <p className="error">{error}</p>;
  if (!rows) return <p className="muted">Loading…</p>;
  const ownedTotal = rows.filter((h) => !h.unvested).reduce((s, h) => s + h.market_value, 0);
  const unvestedTotal = rows.filter((h) => h.unvested).reduce((s, h) => s + h.market_value, 0);
  const total = ownedTotal + (includeUnvested ? unvestedTotal : 0);
  // Owned positions first, then unvested awards.
  const ordered = [...rows.filter((h) => !h.unvested), ...rows.filter((h) => h.unvested)];

  return (
    <>
    <div className="card">
      <div className="card-head">
        <h2>Current positions</h2>
        <span className="sub">
          from each account's latest snapshot · {money(ownedTotal)} owned
          {unvestedTotal > 0 && ` · ${money(unvestedTotal)} unvested (${includeUnvested ? "included" : "not included"} in totals)`}
        </span>
      </div>
      {rows.length === 0 ? (
        <div className="empty">No positions yet. Import a file that includes positions.</div>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Symbol</th><th>Name</th><th>Type</th><th>Account</th>
                <th className="num">Shares</th><th className="num">Price</th><th className="num">Value</th><th className="num">Weight</th><th>As of</th>
              </tr>
            </thead>
            <tbody>
              {ordered.map((h) => (
                <tr key={`${h.account_id}-${h.security_id}-${h.unvested}`} className={h.unvested ? "unvested" : undefined}>
                  <td><strong>{h.symbol ?? "—"}</strong></td>
                  <td className="wrap">{h.security_name}</td>
                  <td>{h.unvested ? <span className="pill unvested" title="Granted but not yet vested; not owned yet">Unvested RSU</span> : <span className="pill" title={h.asset_class ? humanize(h.security_type) : undefined}>{h.asset_class ? h.asset_class.replace(/--/g, " · ") : humanize(h.security_type)}</span>}</td>
                  <td>{h.account_name}</td>
                  <td className="num">{shares(h.quantity)}</td>
                  <td className="num">{h.price != null ? money(h.price) : "—"}</td>
                  <td className="num">{money(h.market_value)}</td>
                  <td className="num">{total && (!h.unvested || includeUnvested) ? percent(h.market_value / total) : "—"}</td>
                  <td className="muted">{shortDate(h.as_of_date)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
    {perf.length > 0 && (
      <div className="card">
        <div className="card-head">
          <h2>Period performance</h2>
          <span className="sub">As reported by fund-summary sources (e.g. 401(k)). Latest period per fund.</span>
        </div>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Fund</th><th>Account</th><th>Period</th>
                <th className="num">Start balance</th><th className="num">Other activity</th><th className="num">Gain / loss</th>
                <th className="num">End balance</th><th className="num">Return</th>
              </tr>
            </thead>
            <tbody>
              {perf.map((p) => (
                <tr key={`${p.account_id}-${p.security_id}`}>
                  <td className="wrap">{p.security_name}{p.asset_class && <div className="muted">{p.asset_class.replace(/--/g, " · ")}</div>}</td>
                  <td>{p.account_name}</td>
                  <td className="muted">{shortDate(p.period_start)} – {shortDate(p.period_end)}</td>
                  <td className="num">{money(p.starting_balance)}</td>
                  <td className="num">{signed(p.other_activity)}</td>
                  <td className={`num ${p.gain_loss > 0 ? "pos" : ""}`}>{signed(p.gain_loss)}</td>
                  <td className="num">{money(p.ending_balance)}</td>
                  <td className="num">{p.return_pct != null ? `${p.return_pct.toFixed(2)}%` : "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <p className="sub" style={{ marginBottom: 0 }}>Start balance is derived (end − gain/loss − other activity). Other activity is net contributions, employer match, fees and transfers.</p>
      </div>
    )}
    </>
  );
}
