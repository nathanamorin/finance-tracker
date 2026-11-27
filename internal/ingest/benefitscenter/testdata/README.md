# Benefits Center 401(k) parser test fixtures — FAKE DATA

**Everything in this directory is fake data.** No file here holds real
plan names, balances or fund positions. Each CSV also carries a
`# FAKE DATA` banner on its first line. The parser ignores everything
before the header row, so the banner also checks that a preamble is
tolerated.

| File | What it is | Used by |
|---|---|---|
| `benefits_center_anonymised_export.csv` | An anonymised real Benefits Center fund summary export: one fund, period Jan 01 to Nov 25, 2026. It shows the real column set, `+$` signed amounts, `%` returns, and units and NAV with 6 decimals. | `benefitscenter_test.go` (`TestAnonymisedExport*`), `internal/store/store_test.go` (`TestBenefitsCenter*`), `internal/api/api_test.go` (`TestUploadBenefitsCenter`) |
| `synthetic_multi_fund.csv` | A hand-written file covering cases the real export doesn't: several funds, negative other activity, a zero-balance fund, a `Total` row (skipped), `--` returns, an unparseable date range (warned and skipped), and repeated spaces in a fund name. | `benefitscenter_test.go` (`TestSynthetic*`) |
