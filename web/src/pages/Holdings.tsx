import { useEffect, useState } from "react";
import { api, type Holding } from "../api";
import { humanize, money, percent, shares, shortDate } from "../format";

export function Holdings() {
  const [rows, setRows] = useState<Holding[]>();
  const [error, setError] = useState<string>();
  useEffect(() => {
    api.holdings().then(setRows).catch((e) => setError(e.message));
  }, []);

  if (error) return <p className="error">{error}</p>;
  if (!rows) return <p className="muted">Loading…</p>;
  const total = rows.reduce((s, h) => s + h.market_value, 0);

  return (
    <div className="card">
      <div className="card-head">
        <h2>Current positions</h2>
        <span className="sub">from each account's latest snapshot · {money(total)}</span>
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
              {rows.map((h) => (
                <tr key={`${h.account_id}-${h.security_id}`}>
                  <td><strong>{h.symbol ?? "—"}</strong></td>
                  <td className="wrap">{h.security_name}</td>
                  <td><span className="pill">{humanize(h.security_type)}</span></td>
                  <td>{h.account_name}</td>
                  <td className="num">{shares(h.quantity)}</td>
                  <td className="num">{h.price != null ? money(h.price) : "—"}</td>
                  <td className="num">{money(h.market_value)}</td>
                  <td className="num">{total ? percent(h.market_value / total) : "—"}</td>
                  <td className="muted">{shortDate(h.as_of_date)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
