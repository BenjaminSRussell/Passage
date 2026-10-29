# Country packs

One directory per pack, `<code>/pack.json`, plus optional `<code>/fixtures/` with statutory-report fixtures.
Packs are metadata only: tax regime, statutory report catalogue, document layouts, bank formats, holiday
region, fiscal calendar, tax-id scheme and minimum retention. They are never code forks (FR-48.1).

The `services/localization` service loads this directory at start-up (`COUNTRY_PACKS_DIR`) and refuses
to start if any pack fails `domain.ValidatePack`. The seven first packs are US, UK, DE, FR, IN, MX and CA (FR-48.2).

Each pack's `version` is `YYYY.n`. An upgrade is a new version of the same code; tax rates change by
effective-dated rows, never by rewriting posted tax (BR-48.10).
