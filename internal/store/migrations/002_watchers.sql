-- Admin-configured watchers: a Discord user pinged for activity at or above
-- a level, org-wide (repo = '') or for one repo. Keyed by the Discord user so
-- a watch survives relinking to another GitHub account.
CREATE TABLE watchers (
    discord_id TEXT NOT NULL,
    repo       TEXT NOT NULL DEFAULT '' COLLATE NOCASE,
    level      TEXT NOT NULL CHECK (level IN ('new-prs', 'prs', 'all')),
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (discord_id, repo)
);

-- Repo names seen in webhook events, for /admin watch autocomplete (the
-- GitHub token can't list private repos).
CREATE TABLE seen_repos (
    name      TEXT PRIMARY KEY COLLATE NOCASE,
    last_seen TEXT NOT NULL
);
