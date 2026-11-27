// Typed client for the Go API (internal/api). Auth is handled upstream by
// the SSO gateway; the browser just sends its session cookie.

export interface Identity { user_id: string; email?: string }

export interface Source { id: string; name: string; description: string }
export interface Meta { sources: Source[]; account_types: string[]; max_upload_mb: number }

export interface AllocationSlice { key: string; label: string; value: number; weight: number }
export interface Summary {
  total_value: number;
  includes_unvested: boolean;
  as_of: string | null;
  account_count: number;
  transaction_count: number;
  net_contributions_all_time: number;
  net_contributions_ytd: number;
  income_ytd: number;
  income_ttm: number;
  last_import_at: string | null;
  unvested_equity_value: number;
  unvested_equity_shares: number;
  next_vest: UpcomingVest | null;
  allocation_by_type: AllocationSlice[];
  allocation_by_class: AllocationSlice[];
  allocation_by_security: AllocationSlice[];
  allocation_by_account: AllocationSlice[];
}

export interface Account {
  id: number;
  institution: string;
  external_id_masked: string;
  name: string;
  account_type: string;
  currency: string;
  closed: boolean;
  balance: number;
  balance_as_of: string | null;
  transaction_count: number;
  first_transaction: string | null;
  last_transaction: string | null;
}

export interface Transaction {
  id: number;
  account_id: number;
  account_name: string;
  security_id: number | null;
  symbol: string | null;
  security_name: string | null;
  trade_date: string;
  settlement_date: string | null;
  type: string;
  source_type: string;
  description: string;
  quantity: number;
  price: number | null;
  amount: number;
  fees: number;
  import_id: number | null;
}

export interface Holding {
  account_id: number;
  account_name: string;
  security_id: number;
  symbol: string | null;
  security_name: string;
  security_type: string;
  asset_class: string | null;
  as_of_date: string;
  quantity: number;
  price: number | null;
  market_value: number;
  unvested: boolean;
}

export interface Performance {
  account_id: number;
  account_name: string;
  security_id: number;
  security_name: string;
  asset_class: string | null;
  period_start: string;
  period_end: string;
  starting_balance: number;
  ending_balance: number;
  gain_loss: number;
  other_activity: number;
  return_pct: number | null;
}

export interface EquityVest {
  period: number;
  vest_date: string;
  granted_qty: number;
  vested_qty: number;
  released_qty: number;
  cancelled_qty: number;
  shares_withheld_for_taxes: number;
  released_amount: number;
  taxes_paid: number;
  tax_status: string | null;
  remaining_qty: number;
  est_value: number;
}
export interface EquityGrant {
  id: number;
  account_name: string;
  symbol: string;
  grant_number: string;
  award_type: string;
  grant_date: string | null;
  status: string | null;
  granted_qty: number;
  vested_qty: number;
  unvested_qty: number;
  withheld_qty: number;
  sellable_qty: number;
  cancelled_qty: number;
  as_of_date: string;
  price: number | null;
  price_date: string | null;
  unvested_value: number;
  vests: EquityVest[];
}
export interface UpcomingVest { vest_date: string; symbol: string; grant_number: string; qty: number; est_value: number }
export interface Equity { grants: EquityGrant[]; unvested_shares: number; unvested_value: number; upcoming: UpcomingVest[] }

export interface Import {
  id: number;
  source: string;
  filename: string;
  imported_at: string;
  holdings_as_of: string | null;
  transactions_parsed: number;
  transactions_inserted: number;
  holdings_parsed: number;
  warnings: string[];
}

export interface ValuePoint { date: string; value: number; net_contributions: number; income: number }
export interface ValueHistory {
  interval: string;
  points: ValuePoint[];
  snapshots: { date: string; value: number }[];
  unpriced_securities: string[];
}

export interface CashFlowRow {
  period: string;
  contributions: number;
  withdrawals: number;
  dividends: number;
  interest: number;
  capital_gains: number;
  fees: number;
  taxes: number;
}

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, { credentials: "same-origin", ...init });
  if (!res.ok) {
    let msg = res.statusText;
    try {
      msg = (await res.json()).error ?? msg;
    } catch {
      /* non-JSON error */
    }
    throw new ApiError(res.status, msg);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

const qs = (params: Record<string, string | number | undefined>) => {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== "" && v !== 0) p.set(k, String(v));
  const s = p.toString();
  return s ? `?${s}` : "";
};

export const api = {
  me: () => request<Identity>("/api/me"),
  meta: () => request<Meta>("/api/meta"),
  summary: (includeUnvested = false) => request<Summary>(`/api/summary${qs({ include_unvested: includeUnvested ? 1 : undefined })}`),
  accounts: () => request<Account[]>("/api/accounts"),
  updateAccount: (id: number, body: Partial<Pick<Account, "name" | "account_type" | "closed">>) =>
    request<void>(`/api/accounts/${id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    }),
  holdings: (includeUnvested = false) => request<Holding[]>(`/api/holdings${qs({ include_unvested: includeUnvested ? 1 : undefined })}`),
  performance: () => request<Performance[]>("/api/performance"),
  equity: () => request<Equity>("/api/equity"),
  transactions: (f: { account_id?: number; type?: string; from?: string; to?: string; q?: string; limit?: number; offset?: number }) =>
    request<{ transactions: Transaction[]; total: number }>(`/api/transactions${qs(f)}`),
  imports: () => request<Import[]>("/api/imports"),
  upload: (file: File, source: string, asOf: string) => {
    const fd = new FormData();
    fd.append("file", file);
    if (source) fd.append("source", source);
    if (asOf) fd.append("as_of", asOf);
    return request<Import>("/api/imports", { method: "POST", body: fd });
  },
  deleteImport: (id: number) => request<void>(`/api/imports/${id}`, { method: "DELETE" }),
  valueHistory: (interval: string, accountId?: number, includeUnvested = false) =>
    request<ValueHistory>(`/api/reports/value-history${qs({ interval, account_id: accountId, include_unvested: includeUnvested ? 1 : undefined })}`),
  cashFlow: (interval: string, accountId?: number) =>
    request<CashFlowRow[]>(`/api/reports/cash-flow${qs({ interval, account_id: accountId })}`),
};
