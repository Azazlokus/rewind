-- Email на аккаунтах + одноразовые токены верификации/сброса (итерация 37).
-- email опционален (NULL — не задан), уникален среди заданных (частичный индекс).
ALTER TABLE accounts ADD COLUMN email TEXT;
ALTER TABLE accounts ADD COLUMN email_verified INTEGER NOT NULL DEFAULT 0;

CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_email ON accounts(email) WHERE email IS NOT NULL;

-- Токены верификации email и сброса пароля. Как и refresh-токены, хранится ТОЛЬКО
-- SHA-256 (token_hash); kind различает назначение (нельзя обменять verify как reset).
-- Одноразовые: used_at 0 — активен, >0 — потрачен (unix-миллисекунды).
CREATE TABLE IF NOT EXISTS account_tokens (
    id         BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at BIGINT NOT NULL,
    used_at    BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_at_account ON account_tokens(account_id);
