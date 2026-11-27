import { useEffect, useState } from "react";
import { api, type Account, type Transaction } from "../api";
import { humanize, money, shares, shortDate } from "../format";

const PAGE = 100;
const TYPES = [
  "buy", "sell", "dividend", "reinvest", "interest", "capital_gain_lt", "capital_gain_st",
  "contribution", "withdrawal", "transfer_in", "transfer_out", "conversion_in", "conversion_out",
  "sweep_in", "sweep_out", "fee", "tax_withholding", "corporate_action", "other",
];

export function Transactions() {
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [filter, setFilter] = useState({ account_id: 0, type: "", from: "", to: "", q: "" });
  const [offset, setOffset] = useState(0);
  const [data, setData] = useState<{ transactions: Transaction[]; total: number }>();
  const [error, setError] = useState<string>();

  useEffect(() => {
    api.accounts().then(setAccounts).catch(() => {});
  }, []);
  useEffect(() => {
    const t = setTimeout(() => {
      api.transactions({ ...filter, limit: PAGE, offset }).then(setData).catch((e) => setError(e.message));
    }, 200); // debounce typing in the search box
    return () => clearTimeout(t);
  }, [filter, offset]);

  const set = (patch: Partial<typeof filter>) => {
    setOffset(0);
    setFilter((f) => ({ ...f, ...patch }));
  };

  return (
    <div className="card">
      <div className="controls">
        <input placeholder="Search description or symbol" value={filter.q} onChange={(e) => set({ q: e.target.value })} />
        <select value={filter.account_id} onChange={(e) => set({ account_id: Number(e.target.value) })} aria-label="Account">
          <option value={0}>All accounts</option>
          {accounts.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
        </select>
        <select value={filter.type} onChange={(e) => set({ type: e.target.value })} aria-label="Type">
          <option value="">All types</option>
          {TYPES.map((t) => <option key={t} value={t}>{humanize(t)}</option>)}
        </select>
        <input type="date" value={filter.from} onChange={(e) => set({ from: e.target.value })} aria-label="From" />
        <input type="date" value={filter.to} onChange={(e) => set({ to: e.target.value })} aria-label="To" />
      </div>
      {error && <p className="error">{error}</p>}
      {data && data.total === 0 && <div className="empty">No transactions match.</div>}
      {data && data.total > 0 && (
        <>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Date</th><th>Type</th><th>Symbol</th><th>Description</th><th>Account</th>
                  <th className="num">Shares</th><th className="num">Price</th><th className="num">Amount</th>
                </tr>
              </thead>
              <tbody>
                {data.transactions.map((t) => (
                  <tr key={t.id}>
                    <td>{shortDate(t.trade_date)}</td>
                    <td><span className="pill" title={`Source label: ${t.source_type}`}>{humanize(t.type)}</span></td>
                    <td>{t.symbol ?? "—"}</td>
                    <td className="wrap">{t.description}{t.security_name ? <div className="muted">{t.security_name}</div> : null}</td>
                    <td>{t.account_name}</td>
                    <td className="num">{t.quantity ? shares(t.quantity) : ""}</td>
                    <td className="num">{t.price ? money(t.price) : ""}</td>
                    <td className={`num ${t.amount > 0 ? "pos" : "neg"}`}>{money(t.amount)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="pager">
            <span className="muted">{offset + 1}–{Math.min(offset + PAGE, data.total)} of {data.total}</span>
            <button className="btn" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))}>Previous</button>
            <button className="btn" disabled={offset + PAGE >= data.total} onClick={() => setOffset(offset + PAGE)}>Next</button>
          </div>
        </>
      )}
    </div>
  );
}
