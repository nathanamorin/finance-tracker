# Finance Tracker

A self-hosted personal finance tracker. You upload exports from your financial
institutions (currently Vanguard, a 401(k) Benefits Center and E*TRADE RSUs). They are parsed into one standard schema and
stored in a **separate, encrypted SQLite database for each user**. The web UI
shows value over time, contributions, income, allocation, positions and a
searchable transaction ledger.

It is built for a cluster where the Istio Gateway gates the app behind
Pocket ID SSO (via oauth2-proxy). The deployment manifests live in
`k8s-deployment/deployments/apps/finance-tracker.yaml`.

## Architecture

```
browser ──► Istio Gateway ──ext_authz──► oauth2-proxy ──► Pocket ID
                │  (injects X-Auth-Request-User / -Email)
                ▼
         finance-tracker (Go, single static binary, UI embedded)
                │
                ├─ users/<sha256(user)>/finance.db   AES-256-XTS encrypted SQLite
                └─ users/<sha256(user)>/dek.json     that DB's key, wrapped by the master key
```

- **Go + Echo** API (`internal/api`), **React + Vite + Recharts** UI (`web/`), shipped
  as one CGO-free binary in a distroless image.
- **Identity** comes from `X-Auth-Request-User`, set by oauth2-proxy (the
  same mechanism as local-podcasts). Unlike local-podcasts, the app **fails
  closed**: a request without that header gets a 401. `FIN_TRACKER_DEV_USER` exists only for
  local development.

## Encryption

Envelope encryption, in `internal/keystore` and `internal/store`:

1. Each user gets a random 64-byte **data key**. It is the AES-256-XTS key
   for their SQLite database, applied page by page by the
   [`ncruces/go-sqlite3`](https://github.com/ncruces/go-sqlite3) `xts` VFS. The
   database, its WAL and its journals are all encrypted, and temp storage is
   kept in memory.
2. The data key is wrapped with **AES-256-GCM** under the server's **master
   key** (`FIN_TRACKER_MASTER_KEY`, a Kubernetes Secret). The user ID is bound in as
   associated data, so a key file swapped into another user's directory
   won't decrypt.
3. Directory names are hashes of the user ID, so the volume reveals neither
   usernames nor data.
4. A user's database is opened when they first make a request and closed
   after `FIN_TRACKER_IDLE_CLOSE` (default 15m) of inactivity.

**Master key rotation:** set the new key as `FIN_TRACKER_MASTER_KEY` and put the old one in
`FIN_TRACKER_PREVIOUS_MASTER_KEYS`. Each user's data key is re-wrapped the
next time their database opens. The databases themselves never need
re-encrypting.

**Threat model:** this protects data at rest: the PVC, volume snapshots and
off-site backups. It does not protect against someone who controls the
running server or the master key. As the VFS docs note, XTS is
deterministic and unauthenticated: an attacker holding several snapshots can
see which pages changed, and the encryption does not detect tampering.
**Losing the master key loses all data.** Keep it in a password manager.

## Standardized schema

`internal/store/migrations/0001_init.sql` (one copy per user database, so there are no `user_id` columns):

| Table | Purpose |
|---|---|
| `accounts` | One row per institution account. Name and type are user-editable. |
| `securities` | Identified by symbol, or by name when there is no symbol. `asset_class` holds the source's own classification when it gives one. |
| `transactions` | A normalized ledger. `type` is one of `buy, sell, dividend, reinvest, interest, capital_gain_lt/st, contribution, withdrawal, transfer_in/out, conversion_in/out, sweep_in/out, fee, tax_withholding, corporate_action, other`. The source's own label is kept in `source_type`. `quantity` is a signed share change; `amount_cents` is the signed effect on cash. `fingerprint` de-duplicates overlapping uploads. |
| `holdings` | Point-in-time position snapshots per account. |
| `account_balances` | Account value on a date. `source` is `holdings` (computed from positions), `reported` (stated by the source) or `derived` (computed from other figures). When two disagree for the same date, holdings beat reported, and reported beats derived. |
| `period_performance` | Per-fund summaries over a date range (gain/loss, other activity, return %) from fund-summary sources such as 401(k) sites. Kept out of `transactions` because successive downloads cover overlapping ranges. |
| `prices` | Known prices per security per day, taken from trades and snapshots. Used to value history. |
| `equity_grants`, `equity_vests` | Employer stock awards (RSUs) and their vesting tranches. **Unvested shares live only here**, never in `holdings` or `account_balances`. They're valued at the latest price, and reports count them only when asked to (see below). Vested, sellable shares are imported as ordinary holdings. |
| `imports`, `import_files` | Each upload with its stats and warnings. The original file is kept (encrypted) so it can be re-parsed later. |

Money is stored as integer cents. Re-uploading the same or an overlapping
file is safe: duplicate transactions are skipped, and a positions snapshot replaces the
earlier one for the same accounts and date. Deleting an import removes
everything that import added.

**Value over time** is reconstructed from transactions, anchored to each
account's latest snapshot:
`qty(d) = snapshotQty + Σdeltas(≤d) − Σdeltas(≤snapshotDate)`.
This counts positions held before your earliest imported transaction, and
the result always agrees with your latest positions. Between trades, each
position is valued at the last known trade price. Adding a market-price
source would make this exact. Accounts with no transactions at all (such as
401(k) fund summaries) can't be reconstructed this way. They use their
recorded balances instead, carried forward from each balance date, and add
nothing before their first balance.

## Data sources

### Vanguard (`vanguard_csv`)

Vanguard → My Accounts → **Download center** → choose accounts and a date range →
**"A spreadsheet-compatible CSV file"** (`OfxDownload.csv`). The file holds a
positions table (no date, so the upload form asks for "positions as of",
defaulting to today) followed by a transactions table. The parser locates
columns by header name and handles details confirmed against a real export:
the `Commissions and Fees` column, `CASH` rows with no symbol, and
settlement-fund sweeps reported as 0 shares (the VMFXX share change is derived
from the dollar amount). Rows it can't classify are stored as `other` and
flagged as warnings on the import.

Test fixtures are in `internal/ingest/vanguard/testdata/`. **All of them are fake data**; see the README there.

### 401(k) Benefits Center (`benefits_center_401k_csv`)

This is the plan's fund summary CSV: `Plan Name, Date Range, Fund Name, Asset
Class, Relative Risk, Balance, % Invested, Future Investments, Gain/Losses,
Other Activity, Fund Returns, Units, Price (NAV)`. It is a **summary, not a
transaction history**, so an import produces:

- a positions snapshot dated at the **end of the file's Date Range** (the
  upload form's as-of date is ignored), using units, NAV and balance per fund;
- one `period_performance` row per fund;
- a derived starting balance at the start of the range (Balance − Gain/Losses −
  Other Activity), so value history has a real data point there.

The plan name serves as the account ID, because the file has no account number.
Use the "All Account" export, which combines every money source. Importing
per-source exports as well would count the same money twice. "Other
Activity" is not added to net contributions, because year-to-date downloads overlap.

Fixtures are in `internal/ingest/benefitscenter/testdata/`. **All of them are fake data**; see the README there.

### E*TRADE Restricted Stock (`etrade_restricted_stock_xlsx`)

E*TRADE → Stock Plan → Holdings → Restricted Stock → Download → **Download
Expanded** (`.xlsx`). The sheet is hierarchical: column A, `Record Type`,
marks `Grant`, `Vest Schedule` (tranches, linked by `Grant Number`) and
`Totals` rows. Grant and tranche rows use different column blocks with
repeated names, so the parser looks columns up *within each block*. An
import produces:

- an `E*TRADE Stock Plan` account (type `stock_plan`);
- one equity award per grant, with its tranches (dates, scheduled, vested,
  released and cancelled shares, taxes);
- a share price implied by Est. Market Value ÷ outstanding shares, on the
  upload's as-of date. The file has no date of its own;
- a holding for **vested, sellable** shares, if there are any.

Unvested shares appear on the Equity tab, in a dashboard tile, and as
"Unvested RSU" rows on Holdings. An **Include unvested** switch in the header
(on by default, remembered per browser) adds them to total value, the
allocation panels and value over time. In the history, each tranche counts
from its grant date until it vests. They never count toward "value minus
contributions". In the API, add `?include_unvested=1` to `/api/summary`,
`/api/holdings` and `/api/reports/value-history`. The default excludes them. Releases are recorded on the tranches but don't become
transactions, so a brokerage transaction export would be needed for those.
Other record types, such as tax withholding and dividends, are reported as
warnings. The anonymised sample had no vested shares, so the released and
tax columns rely on assumptions that the synthetic fixture exercises.

The `.xlsx` reader (`internal/ingest/xlsxutil`) is a small standard-library
zip and XML reader with a 64 MB decompression cap, so there's no third-party
spreadsheet dependency.

Fixtures are in `internal/ingest/etrade/testdata/`. **All of them are fake
data**: the anonymised export has an added `FAKE DATA` sheet, and the
synthetic workbook (generated by `gen_synthetic.py`) has a banner row. See the README there.

### Adding a source

1. Create `internal/ingest/<name>/` with a type that implements `ingest.Source`.
   `internal/ingest/csvutil` has lenient number, date and date-range parsing and header
   lookup; `internal/ingest/xlsxutil` reads `.xlsx` files. `Detect` receives the whole file.
   with `ID`, `Name`, `Description`, `Detect` and `Parse`. It returns an `ingest.Result`
   in the standard model and follows the sign conventions documented on
   `ingest.Transaction`. Parsers never touch the database.
2. Register it in `cmd/server/main.go` (`ingest.NewRegistry(...)`).
3. Add fixtures (with a `# FAKE DATA` banner on the first line, listed in a
   `testdata/README.md`) and tests.

Good next candidates: OFX/QFX (which covers most banks and brokerages), Fidelity
and Schwab CSVs, and bank or credit card CSVs (the `category` column is
reserved for these).

## Development

```bash
make dev        # API on :8080 as user "dev"; a local master key is generated in .data/
make web-dev    # Vite on :5173 with hot reload, proxying /api to :8080
make test       # go vet, go test, frontend typecheck
make docker     # build the production image
```

## Configuration

| Variable | Default | |
|---|---|---|
| `FIN_TRACKER_MASTER_KEY` | (required) | base64, 32 bytes: `openssl rand -base64 32` |
| `FIN_TRACKER_PREVIOUS_MASTER_KEYS` | | Retired master keys, comma-separated, for rotation |
| `FIN_TRACKER_DATA_DIR` | `/data` | |
| `FIN_TRACKER_ADDR` | `:8080` | |
| `FIN_TRACKER_USER_HEADER` | `X-Auth-Request-User` | |
| `FIN_TRACKER_EMAIL_HEADER` | `X-Auth-Request-Email` | |
| `FIN_TRACKER_DEV_USER` | | **Local development only.** The user assumed when the SSO header is missing. |
| `FIN_TRACKER_MAX_UPLOAD_MB` | `25` | |
| `FIN_TRACKER_IDLE_CLOSE` | `15m` | |
| `TZ` | | Sets what "today" means for position snapshots and year-to-date figures |

## API

All endpoints under `/api` require SSO.
`GET /api/me`, `/api/meta`, `/api/summary`, `/api/accounts`, `PATCH /api/accounts/:id`,
`GET /api/transactions?account_id&type&from&to&q&limit&offset`, `/api/holdings`, `/api/performance`, `/api/equity`,
`/api/securities`, `/api/imports`, `POST /api/imports` (multipart: `file`, optional
`source`, `as_of`), `GET /api/imports/:id/file`, `DELETE /api/imports/:id`,
`GET /api/reports/value-history?interval=day|week|month`, `/api/reports/cash-flow?interval=month|year`.
`GET /healthz` is unauthenticated.

## CI

GitHub Actions: `test.yml` runs Go and frontend checks. `docker-publish.yml`
builds and pushes `ghcr.io/nathanamorin/finance-tracker` and signs it with cosign
(tags: branch, semver and `sha-<short>`). Dependabot keeps dependencies
current and auto-merges minor and patch updates.
