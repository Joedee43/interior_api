-- Each company prices everything in one ISO 4217 currency, so a quote
-- never mixes currencies. Product prices are stored without a currency
-- and are interpreted in their company's currency.

-- +goose Up
ALTER TABLE companies
    ADD COLUMN currency char(3) NOT NULL DEFAULT 'ETB',
    ADD CONSTRAINT companies_currency_check CHECK (currency ~ '^[A-Z]{3}$');

-- +goose Down
ALTER TABLE companies DROP COLUMN currency;
