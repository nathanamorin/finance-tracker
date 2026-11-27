const usd = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" });
const usdWhole = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 0 });
const usdCompact = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", notation: "compact", maximumFractionDigits: 1 });
const num = new Intl.NumberFormat("en-US", { maximumFractionDigits: 4 });
const pct = new Intl.NumberFormat("en-US", { style: "percent", maximumFractionDigits: 1 });

export const money = (v: number) => usd.format(v);
export const moneyWhole = (v: number) => usdWhole.format(v);
export const moneyCompact = (v: number) => usdCompact.format(v);
export const shares = (v: number) => num.format(v);
export const percent = (v: number) => pct.format(v);

export const humanize = (s: string) =>
  s
    .replace(/_/g, " ")
    .replace(/\b(lt|st)\b/g, (m) => `(${m.toUpperCase()})`)
    .replace(/\bira\b/gi, "IRA")
    .replace(/\betf\b/gi, "ETF")
    .replace(/^./, (c) => c.toUpperCase());

export const shortDate = (iso: string) => {
  const [y, m, d] = iso.split("-").map(Number);
  return new Date(y, m - 1, d).toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" });
};

export const monthLabel = (iso: string) => {
  const [y, m] = iso.split("-").map(Number);
  return new Date(y, m - 1, 1).toLocaleDateString("en-US", { month: "short", year: "2-digit" });
};

export const timestamp = (rfc3339: string) => new Date(rfc3339).toLocaleString();
