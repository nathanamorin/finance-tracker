import { Fragment, useEffect, useState } from "react";
import { api, type Equity } from "../api";
import { VestChart } from "../charts";
import { money, moneyWhole, shares, shortDate } from "../format";

export function EquityPage() {
  const [eq, setEq] = useState<Equity>();
  const [error, setError] = useState<string>();
  useEffect(() => {
    api.equity().then(setEq).catch((e) => setError(e.message));
  }, []);

  if (error) return <p className="error">{error}</p>;
  if (!eq) return <p className="muted">Loading…</p>;
  if (eq.grants.length === 0) {
    return <div className="card empty">No equity awards yet. Import an E*TRADE Restricted Stock download.</div>;
  }

  const next = eq.upcoming[0];
  const sellable = eq.grants.reduce((s, g) => s + g.sellable_qty, 0);
  const price = eq.grants.find((g) => g.price != null);

  return (
    <>
      <div className="tiles">
        <div className="tile">
          <div className="label">Unvested value</div>
          <div className="value">{moneyWhole(eq.unvested_value)}</div>
          <div className="hint">pre-tax estimate · counted in totals when "Include unvested" is on</div>
        </div>
        <div className="tile">
          <div className="label">Unvested shares</div>
          <div className="value">{shares(eq.unvested_shares)}</div>
          <div className="hint">{sellable > 0 ? `${shares(sellable)} vested & sellable (in holdings)` : "none vested & sellable"}</div>
        </div>
        <div className="tile">
          <div className="label">Next vest</div>
          <div className="value">{next ? shortDate(next.vest_date) : "—"}</div>
          <div className="hint">{next ? `${shares(next.qty)} ${next.symbol} · ~${moneyWhole(next.est_value)}` : "nothing scheduled"}</div>
        </div>
        <div className="tile">
          <div className="label">Price used</div>
          <div className="value">{price?.price != null ? money(price.price) : "—"}</div>
          <div className="hint">{price?.price_date ? `${price.symbol} as of ${shortDate(price.price_date)}` : "no price known"}</div>
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <h2>Vesting schedule</h2>
          <span className="sub">Estimated value vesting on each date, at the price above, before tax withholding</span>
        </div>
        <VestChart upcoming={eq.upcoming} />
      </div>

      <div className="card">
        <div className="card-head"><h2>Grants</h2><span className="sub">Each grant, with its vesting tranches indented below</span></div>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Grant</th><th>Type</th><th>Granted</th><th>Vest date</th><th>Status</th>
                <th className="num">Shares</th><th className="num">Vested</th><th className="num">Unvested / remaining</th>
                <th className="num">Cancelled</th><th className="num">Est. value</th>
              </tr>
            </thead>
            <tbody>
              {eq.grants.map((g) => (
                <Fragment key={g.id}>
                  <tr>
                    <td><strong>{g.symbol}</strong> <span className="muted">{g.grant_number}</span></td>
                    <td><span className="pill">{g.award_type}</span></td>
                    <td>{g.grant_date ? shortDate(g.grant_date) : "—"}</td>
                    <td />
                    <td className="muted">{g.status ?? ""}</td>
                    <td className="num">{shares(g.granted_qty)}</td>
                    <td className="num">{shares(g.vested_qty)}</td>
                    <td className="num">{shares(g.unvested_qty)}</td>
                    <td className="num">{g.cancelled_qty ? shares(g.cancelled_qty) : ""}</td>
                    <td className="num">{money(g.unvested_value)}</td>
                  </tr>
                  {g.vests.map((v) => (
                    <tr key={`${g.id}-${v.period}-${v.vest_date}`} className="muted">
                      <td style={{ paddingLeft: 28 }}>Tranche {v.period || ""}</td>
                      <td />
                      <td />
                      <td>{shortDate(v.vest_date)}</td>
                      <td>{v.tax_status ?? ""}</td>
                      <td className="num">{shares(v.granted_qty)}</td>
                      <td className="num">{v.vested_qty ? shares(v.vested_qty) : ""}</td>
                      <td className="num">{v.remaining_qty ? shares(v.remaining_qty) : ""}</td>
                      <td className="num">{v.cancelled_qty ? shares(v.cancelled_qty) : ""}</td>
                      <td className="num">{v.est_value ? money(v.est_value) : ""}</td>
                    </tr>
                  ))}
                </Fragment>
              ))}
            </tbody>
          </table>
        </div>
        <p className="sub" style={{ marginBottom: 0 }}>
          Values use the share price implied by E*TRADE's estimated market value on the import date. Taxes withheld at vest will reduce what you actually receive.
        </p>
      </div>
    </>
  );
}
