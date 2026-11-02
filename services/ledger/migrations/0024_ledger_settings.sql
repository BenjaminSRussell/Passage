ALTER TABLE ledger
    ADD COLUMN IF NOT EXISTS calendar_code        text CHECK (calendar_code IS NULL OR calendar_code ~ '^[A-Z0-9_-]{1,20}$'),
    ADD COLUMN IF NOT EXISTS accounting_principle text CHECK (accounting_principle IS NULL OR accounting_principle IN ('IFRS', 'US_GAAP', 'LOCAL_GAAP', 'TAX', 'MANAGEMENT')),
    ADD COLUMN IF NOT EXISTS reporting_currency   text CHECK (reporting_currency IS NULL OR reporting_currency ~ '^[A-Z]{3}$'),
    ADD COLUMN IF NOT EXISTS settings_version     int  NOT NULL DEFAULT 1 CHECK (settings_version >= 1);

ALTER TABLE journal_line ADD COLUMN IF NOT EXISTS rounding_adjustment boolean NOT NULL DEFAULT false;
