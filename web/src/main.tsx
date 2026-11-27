import { StrictMode, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import { api, type Identity, type Meta } from "./api";
import { Accounts } from "./pages/Accounts";
import { Dashboard } from "./pages/Dashboard";
import { EquityPage } from "./pages/Equity";
import { Holdings } from "./pages/Holdings";
import { Imports } from "./pages/Imports";
import { Transactions } from "./pages/Transactions";
import "./styles.css";

const TABS = ["dashboard", "holdings", "equity", "transactions", "accounts", "import"] as const;
type Tab = (typeof TABS)[number];

// Per-browser preference; storage can be unavailable (private mode), so
// every access is guarded and the default applies.
const UNVESTED_KEY = "include-unvested";
const loadIncludeUnvested = () => {
  try {
    return localStorage.getItem(UNVESTED_KEY) !== "0";
  } catch {
    return true;
  }
};

const tabFromHash = (): Tab => {
  const h = location.hash.replace("#", "") as Tab;
  return TABS.includes(h) ? h : "dashboard";
};

function App() {
  const [tab, setTab] = useState<Tab>(tabFromHash);
  const [me, setMe] = useState<Identity>();
  const [meta, setMeta] = useState<Meta>();
  const [error, setError] = useState<string>();
  // Bumped after imports so other tabs re-fetch when shown.
  const [version, setVersion] = useState(0);
  const [includeUnvested, setIncludeUnvested] = useState(loadIncludeUnvested);
  const toggleUnvested = (v: boolean) => {
    setIncludeUnvested(v);
    try {
      localStorage.setItem(UNVESTED_KEY, v ? "1" : "0");
    } catch {
      /* preference just won't persist */
    }
  };

  useEffect(() => {
    api.me().then(setMe).catch((e) => setError(e.message));
    api.meta().then(setMeta).catch((e) => setError(e.message));
    const onHash = () => setTab(tabFromHash());
    addEventListener("hashchange", onHash);
    return () => removeEventListener("hashchange", onHash);
  }, []);

  const go = (t: Tab) => {
    location.hash = t;
    setTab(t);
  };

  return (
    <div className="app">
      <header className="top">
        <h1>Finance Tracker</h1>
        <nav className="tabs">
          {TABS.map((t) => (
            <button key={t} aria-current={tab === t ? "page" : undefined} onClick={() => go(t)}>
              {t[0].toUpperCase() + t.slice(1)}
            </button>
          ))}
        </nav>
        <label className="toggle" title="Count unvested RSUs (at the latest price) in totals, allocation and value history">
          <input type="checkbox" checked={includeUnvested} onChange={(e) => toggleUnvested(e.target.checked)} />
          Include unvested
        </label>
        {me && <span className="user" title={me.user_id}>{me.email || me.user_id}</span>}
      </header>
      {error && <p className="error">{error}</p>}
      {meta && (
        <main key={`${tab}-${version}-${includeUnvested}`}>
          {tab === "dashboard" && <Dashboard goImport={() => go("import")} includeUnvested={includeUnvested} />}
          {tab === "holdings" && <Holdings includeUnvested={includeUnvested} />}
          {tab === "equity" && <EquityPage />}
          {tab === "transactions" && <Transactions />}
          {tab === "accounts" && <Accounts accountTypes={meta.account_types} />}
          {tab === "import" && <Imports meta={meta} onImported={() => setVersion((v) => v + 1)} />}
        </main>
      )}
    </div>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
