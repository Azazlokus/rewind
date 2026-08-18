-- Агрегированная античит-статистика по аккаунту (итерация 40). Одна строка на
-- (account_id, kind); count копится персистером по событиям из игры. Времена —
-- unix-миллисекунды. Порог по сумме → автобан (см. internal/persist).
CREATE TABLE IF NOT EXISTS anticheat_stats (
    account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    count      INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (account_id, kind)
);

CREATE INDEX IF NOT EXISTS idx_ac_account ON anticheat_stats(account_id);
