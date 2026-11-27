# Vanguard parser test fixtures — FAKE DATA

**Everything in this directory is fake data.** No file here holds real
account numbers, balances or transactions. Each CSV also carries a
`# FAKE DATA` banner on its first line. The parser ignores lines like that,
so the banner also checks that junk before the first table is tolerated.

| File | What it is | Used by |
|---|---|---|
| `vanguard_anonymised_export.csv` | An anonymised real Vanguard Download Center export (two accounts, about 460 transactions, 2025-05 to 2026-11). It shows the current column names (`Commissions and Fees`), `CASH` rows with no symbol, and settlement-fund sweeps with 0 shares. | `vanguard_test.go` (`TestAnonymisedExport*`), `internal/store/store_test.go` (end-to-end import and report tests) |
| `synthetic_edge_cases.csv` | A hand-written file covering edge cases the real export doesn't: UTF-8 BOM, US-format dates, `($1,234.56)` amounts, the older `Commission Fees` header, duplicate identical rows, unknown transaction types and a footer disclaimer. | `vanguard_test.go` (`parseFixture`, `TestParse*`, `TestDetect`) |
