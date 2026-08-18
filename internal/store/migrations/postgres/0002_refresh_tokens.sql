-- refresh-токены (итерация 36). Хранится ТОЛЬКО SHA-256 открытого токена (token_hash):
-- на сервере plaintext-токена нет. family_id — цепочка ротаций одного логина; повторное
-- предъявление отозванного токена гасит всё семейство (детект кражи). revoked_at: 0 —
-- активен, >0 — отозван (unix-миллисекунды, единый с остальной схемой маппинг времени).
CREATE TABLE IF NOT EXISTS refresh_tokens (
    id         BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    family_id  TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    issued_at  BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    revoked_at BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_rt_family ON refresh_tokens(family_id);
CREATE INDEX IF NOT EXISTS idx_rt_account ON refresh_tokens(account_id);
