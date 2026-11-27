import { useEffect, useState } from "react";
import { api, type Account } from "../api";
import { humanize, money, shortDate } from "../format";

export function Accounts({ accountTypes }: { accountTypes: string[] }) {
  const [rows, setRows] = useState<Account[]>();
  const [error, setError] = useState<string>();
  const load = () => api.accounts().then(setRows).catch((e) => setError(e.message));
  useEffect(() => {
    load();
  }, []);

  const save = async (a: Account, patch: Partial<Account>) => {
    try {
      await api.updateAccount(a.id, patch);
      load();
    } catch (e) {
      setError((e as Error).message);
    }
  };

  if (!rows) return <p className="muted">Loading…</p>;
  return (
    <div className="card">
      <div className="card-head">
        <h2>Accounts</h2>
        <span className="sub">Accounts are created automatically on import. Rename them and set their type here.</span>
      </div>
      {error && <p className="error">{error}</p>}
      {rows.length === 0 ? (
        <div className="empty">No accounts yet.</div>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Name</th><th>Institution</th><th>Number</th><th>Type</th>
                <th className="num">Balance</th><th>History</th><th className="num">Transactions</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((a) => (
                <tr key={a.id}>
                  <td>
                    <input
                      defaultValue={a.name}
                      aria-label="Account name"
                      onBlur={(e) => e.target.value.trim() && e.target.value !== a.name && save(a, { name: e.target.value })}
                      onKeyDown={(e) => e.key === "Enter" && (e.target as HTMLInputElement).blur()}
                    />
                  </td>
                  <td>{a.institution}</td>
                  <td className="muted">{a.external_id_masked}</td>
                  <td>
                    <select value={a.account_type} onChange={(e) => save(a, { account_type: e.target.value })} aria-label="Account type">
                      {accountTypes.map((t) => <option key={t} value={t}>{humanize(t)}</option>)}
                    </select>
                  </td>
                  <td className="num">
                    {money(a.balance)}
                    {a.balance_as_of && <div className="muted">{shortDate(a.balance_as_of)}</div>}
                  </td>
                  <td className="muted">
                    {a.first_transaction && a.last_transaction ? `${shortDate(a.first_transaction)} – ${shortDate(a.last_transaction)}` : "—"}
                  </td>
                  <td className="num">{a.transaction_count}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
