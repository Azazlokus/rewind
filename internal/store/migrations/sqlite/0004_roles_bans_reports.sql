-- Роли, баны и репорты (итерация 39). Роль — 'user'|'moderator'|'admin'.
ALTER TABLE accounts ADD COLUMN role TEXT NOT NULL DEFAULT 'user';

-- Баны: история записей; активный бан — lifted_at=0 И (expires_at=0 ИЛИ expires_at>now).
-- Времена — unix-миллисекунды; 0 — «навсегда»/«активен» (единый маппинг схемы).
CREATE TABLE IF NOT EXISTS bans (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    reason     TEXT NOT NULL,
    created_by INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL DEFAULT 0,
    lifted_at  INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_bans_account ON bans(account_id);

-- Репорты игроков: reporter жалуется на target; статус 'open'|'reviewed'.
CREATE TABLE IF NOT EXISTS reports (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    reporter_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    target_id   INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    reason      TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    status      TEXT NOT NULL DEFAULT 'open'
);

CREATE INDEX IF NOT EXISTS idx_reports_status ON reports(status);
CREATE INDEX IF NOT EXISTS idx_reports_target ON reports(target_id);
