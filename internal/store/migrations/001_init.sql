-- Links a GitHub account to a Discord user. Both sides are keyed by their
-- immutable numeric IDs; github_login is display-only and kept fresh.
CREATE TABLE links (
    github_id    INTEGER PRIMARY KEY,
    github_login TEXT    NOT NULL COLLATE NOCASE,
    discord_id   TEXT    NOT NULL UNIQUE,
    created_by   TEXT    NOT NULL,
    created_at   TEXT    NOT NULL,
    updated_at   TEXT    NOT NULL
);

-- GitHub users that must never be pinged.
CREATE TABLE ignored (
    github_id    INTEGER PRIMARY KEY,
    github_login TEXT    NOT NULL COLLATE NOCASE,
    created_by   TEXT    NOT NULL,
    created_at   TEXT    NOT NULL
);

-- Cache of login -> numeric ID lookups for @mentions.
CREATE TABLE login_cache (
    github_login TEXT    PRIMARY KEY COLLATE NOCASE,
    github_id    INTEGER NOT NULL,
    fetched_at   TEXT    NOT NULL
);
CREATE INDEX login_cache_github_id ON login_cache (github_id);
