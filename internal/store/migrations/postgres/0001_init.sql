-- Начальная схема (PostgreSQL). Таймстемпы — unix-миллисекунды (BIGINT), булевы —
-- INTEGER 0/1: единый с SQLite маппинг, чтобы сканирование не зависело от драйвера.
CREATE TABLE IF NOT EXISTS accounts (
    id            BIGSERIAL PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS stats (
    account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    kills      BIGINT NOT NULL DEFAULT 0,
    deaths     BIGINT NOT NULL DEFAULT 0,
    games      BIGINT NOT NULL DEFAULT 0,
    wins       BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS matches (
    id         BIGSERIAL PRIMARY KEY,
    mode       TEXT NOT NULL,
    seed       BIGINT NOT NULL,
    started_at BIGINT NOT NULL,
    ended_at   BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS match_participants (
    match_id   BIGINT NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    kills      INTEGER NOT NULL DEFAULT 0,
    deaths     INTEGER NOT NULL DEFAULT 0,
    won        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (match_id, account_id)
);

CREATE INDEX IF NOT EXISTS idx_mp_account ON match_participants(account_id);
