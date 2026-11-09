# pingcord — Implementation Plan

> Ping the right developer in Discord when GitHub needs them: @mentions,
> review requests, assignments, and activity on their own PRs. Nobody else gets pinged.

Status: **live** at `pingcord.lexerlabs.com`; reviewed (code + security), all CRITICAL/HIGH/MEDIUM findings fixed · Last updated: 2026-11-10

---

## 1. Problem

- A team of 5–10 devs works in one GitHub org.
- GitHub notifications are easy to miss for people who don't use GitHub Mobile.
- A plain GitHub → Discord webhook posts *everything*. That works for the team lead, who
  needs to see it all, but it's noise for everyone else.
- **Goal:** one private Discord channel the whole team is in, where each message @pings
  **only** the people the GitHub event concerns.

## 2. Goals / Non-goals

**Goals**
- Ping on: @mentions (anywhere in PR/issue/comment/review/discussion text), review
  requests (user and team), assignments, reviews on your PR, comments on your PR/issue.
- The events you tick in GitHub's webhook settings decide what's on. There's no notify config.
- The GitHub ↔ Discord mapping survives renames on both platforms.
- Admins manage the mapping from Discord, with no redeploy.
- Self-hosted: one static binary under systemd behind the server's shared host Caddy, auto-deployed from GitHub Actions (Docker optional). Public, open-source repo.

**Non-goals (for now)**
- Self-service `/link` for non-admins (spoofing risk).
- Per-user DMs and per-user notification preferences.
- Multi-org or multi-Discord-server support.
- A web UI.

## 3. Architecture

```
GitHub org webhook ──POST /github/webhook──┐
                                           ▼
Discord slash cmds ─POST /discord/interactions─▶  host Caddy (HTTPS) ─▶ 127.0.0.1:8090 pingcord (Go, systemd)
                                                                   │  ├─ verify signatures
                                                                   │  ├─ event → recipients
                                                                   │  ├─ SQLite (links, ignores, cache)
                                                                   │  └─ GitHub API (org/team members, login→id)
                                                                   ▼
                                                     Discord channel webhook (pings only recipients)
```

- **One org-level GitHub webhook** covers every repo.
- **One Discord channel webhook** for output. Its `allowed_mentions.users` lists exactly the
  intended recipients.
- **One Discord application**, used only for the `/admin` slash commands over HTTP. No
  always-on bot connection is needed.

## 4. Identity mapping

| Side | Stable identifier | Changes? |
|---|---|---|
| GitHub | numeric user ID (`user.id`) | never (the username can change) |
| Discord | user ID (snowflake) | never (usernames and display names can change) |

- Links are stored as **GitHub numeric ID ↔ Discord user ID**.
- Users named in the payload (`sender`, `requested_reviewer`, `assignee`, `pull_request.user`,
  …) come with their numeric ID, so they match directly.
- Users @mentioned in text appear only as usernames. The server resolves them with
  `GET /users/{username}` and caches the result in `login_cache`.
- Every incoming payload updates the stored username for each ID it contains, so GitHub
  renames are picked up without any action.

## 5. Recipient rules

### 5.1 Catch-all: works for any event type
Scan every text field in the payload (`body`, `description`, `title` where relevant) for
`@username` and `@org/team`. Mentions in Discussions, commit comments, releases and any
future event type work without extra code.

### 5.2 Specific handlers: pings that come from a role, not an @mention
| Event / action | Who gets pinged | Reason label |
|---|---|---|
| `pull_request.review_requested` | requested user, or members of the requested team | review requested |
| `pull_request.assigned`, `issues.assigned` | assignee | assigned |
| `pull_request_review.submitted` (approved / changes_requested) | PR author | review on your PR |
| `pull_request_review.submitted` (commented) | PR author | comment on your PR |
| `issue_comment.created` on a PR | PR author | comment on your PR |
| `issue_comment.created` on an issue | issue author | comment on your issue |

### 5.3 Noise rules
1. **No self-pings.** The person who triggered the event is never pinged.
2. **Edits ping only new mentions.** On `*.edited`, the server compares `changes.body.from`
   with the new body and pings only mentions that weren't there before.
3. **No inline-comment storms.** `pull_request_review_comment` never pings the PR author;
   the matching `pull_request_review.submitted` event does that once.
4. **Mention parsing follows GitHub's rules.** `@` inside fenced code, inline code, quoted
   `>` reply lines, and email addresses is ignored.
5. **Skipped:** unlinked users (logged), ignored users, and bots (`[bot]` suffix,
   `type: Bot`).
6. **Releases ping once:** only `release.published` pings (GitHub also sends
   `created` for drafts and alongside a direct publish).
7. **Bounded:** at most 25 @users and 5 @teams are resolved per event; team
   mentions outside `GITHUB_ORG` are ignored; events from other orgs are ignored.
8. **Grouped:** each GitHub event produces one Discord message, one ping per person, with all
   reasons combined: `@alice (review requested, mentioned)`.

### 5.4 Message format
- `content`: the pings and reasons (≤ 2000 chars).
- `embed`: repo · `#number title` (linked) · who did it · a short excerpt (≤ 300 chars,
  mentions defanged so the excerpt can't ping anyone) · event type in the footer.
- `allowed_mentions`: `{ "parse": [], "users": [<recipient ids>] }`. Only recipients can be
  pinged, never `@everyone` or roles.

## 6. Admin commands (Discord)

Visible only to members with **Manage Server**, and also checked on the server against
`ADMIN_DISCORD_IDS`.

| Command | What it does |
|---|---|
| `/admin link user:@Discord github:<username>` | Create or replace a link. `github` autocompletes from the org member list. |
| `/admin unlink user:@Discord` | Remove a link |
| `/admin list` | Show all links (GitHub username ↔ Discord user) |
| `/admin unlinked` | Org members with no link yet (the onboarding checklist) |
| `/admin ignore github:<username>` / `/admin unignore` | Never ping this GitHub user |

- Replies are ephemeral (only the admin sees them).
- Every change is also announced in the channel for an audit trail.
- Commands are registered with the guild on startup using `DISCORD_BOT_TOKEN`.

## 7. Configuration

Environment only. There's no config file, and data lives in SQLite.

| Var | Required | Purpose |
|---|---|---|
| `GITHUB_WEBHOOK_SECRET` | ✓ | HMAC secret shared with the GitHub webhook (`openssl rand -hex 32`) |
| `GITHUB_TOKEN` | ✓ | Fine-grained PAT, **Organization → Members: Read-only**. Used for org members, team members and username→ID lookups |
| `GITHUB_ORG` | ✓ | Org login |
| `DISCORD_WEBHOOK_URL` | ✓ | Channel webhook that receives the notifications |
| `DISCORD_APP_ID` | ✓ | Discord application ID |
| `DISCORD_PUBLIC_KEY` | ✓ | Ed25519 key for checking interaction signatures |
| `DISCORD_BOT_TOKEN` | ✓ | Only used to register slash commands |
| `DISCORD_GUILD_ID` | ✓ | Server the commands are registered in |
| `ADMIN_DISCORD_IDS` | ✓ | Comma-separated Discord user IDs |
| `DB_PATH` | | Default `/data/pingcord.db` |
| `PORT` | | Default `8080` |
| `IGNORE_BOTS` | | Default `true` |
| `LOG_LEVEL` | | Default `info` |

The server checks all required vars at startup and exits with a clear error if any are
missing. Secret values are never logged.

## 8. Data model (SQLite)

```sql
CREATE TABLE links (
  github_id      INTEGER PRIMARY KEY,
  github_login   TEXT    NOT NULL,          -- kept up to date from payloads
  discord_id     TEXT    NOT NULL UNIQUE,
  created_by     TEXT    NOT NULL,          -- admin discord id
  created_at     TEXT    NOT NULL,
  updated_at     TEXT    NOT NULL
);
CREATE TABLE ignored (
  github_id      INTEGER PRIMARY KEY,
  github_login   TEXT    NOT NULL,
  created_by     TEXT    NOT NULL,
  created_at     TEXT    NOT NULL
);
CREATE TABLE login_cache (
  github_login   TEXT    PRIMARY KEY COLLATE NOCASE,
  github_id      INTEGER NOT NULL,
  fetched_at     TEXT    NOT NULL           -- refresh after 7 days
);
```
Migrations are embedded SQL files run in order at startup (`schema_version` table).
Team member lists are cached in memory for 10 minutes, not in the database.

## 9. Security

- **GitHub:** every request must have a valid `X-Hub-Signature-256`, checked with HMAC-SHA256
  and a constant-time comparison. Anything else gets a 401.
- **Discord:** every request must have a valid Ed25519 signature
  (`X-Signature-Ed25519` + `X-Signature-Timestamp`). Anything else gets a 401. Discord
  rejects the endpoint during setup if this check is missing.
- **Admin check:** commands are hidden from non-admins in Discord *and* the server checks
  the caller against `ADMIN_DISCORD_IDS`.
- **Request size:** bodies are capped (GitHub's maximum is 25 MB).
- **Timeouts:** every outbound call has one: GitHub API 10 s, Discord 10 s, /admin commands 2.5 s (Discord needs a reply within 3 s).
- **No leaks:** error responses never include internal details.
- **Tokens:** the GitHub token is read-only and limited to member access.
- **SQL:** parameterized queries only.
- **Process:** systemd `DynamicUser` with strict sandboxing (only its state dir is writable).
  Docker alternative: distroless, non-root, read-only root filesystem, data
  volume at `/data`.
- **Network:** the app listens on 127.0.0.1 only; the host Caddy is the only way in.

## 10. Reliability

- **Delivery:** handling is synchronous. If Discord fails, the server returns 502, GitHub
  marks the delivery failed, and an admin clicks **Redeliver**.
- **Discord rate limits:** on a 429, wait `retry_after` and retry once.
- **GitHub API failure:** if a username lookup fails, that one recipient is skipped and
  logged. The rest of the message still goes out.
- **Logging:** structured JSON via `log/slog`, tagged with the GitHub delivery ID.
- **Endpoints:** `GET /healthz` for liveness. The GitHub `ping` event returns 200.
- **Shutdown:** stops cleanly on SIGTERM.

## 11. Tech stack

- **Go** (latest stable). The work is mostly network I/O, and the standard library covers
  HTTP, HMAC, Ed25519, JSON and slog.
- **SQLite** via `modernc.org/sqlite` (pure Go, no cgo: one static binary).
- **No web framework.** `net/http` with Go 1.22+ routing patterns.
- **Caddy** on the host for automatic HTTPS, shared with other apps via `/etc/caddy/sites/*.caddy`.
- **GitHub Actions** for CI and deploy (build in Actions, SSH to the server, health check with rollback).
- **Testing:** standard `testing` plus `httptest`, with GitHub sample payloads in
  `internal/githubhook/testdata/`.

## 12. Project layout

```
cmd/pingcord/main.go        startup: env, wiring, graceful shutdown
internal/config/               env loading + validation
internal/mention/              @user / @org/team extraction (code/quote/email aware)
internal/githubhook/           signature check, payload walker, event → []Recipient
internal/githubapi/            org members, team members, username→ID (with caching)
internal/store/                SQLite repository: links, ignored, login_cache, migrations
internal/notify/               resolve → filter → group → build Discord message
internal/discord/              webhook client (timeout, 429 retry)
internal/discordcmd/           interactions: Ed25519 check, /admin handlers, autocomplete, registration
internal/server/               HTTP routes + middleware (body limit, logging, recover)
internal/githubhook/testdata/  sample webhook payloads
deploy/                        systemd unit, Caddy site, install.sh, deploy guide
.github/workflows/             ci.yml (reusable), deploy.yml (push to main)
Dockerfile, docker-compose.yml optional Docker run on 127.0.0.1:8090
.env.example
README.md                      setup guide
LICENSE                        MIT
```

## 13. Build phases (tests first in each phase)

| # | Phase | Done when |
|---|---|---|
| 0 | Scaffold: module, layout, Makefile (`test`, `lint`, `cover`), CI (`go vet`, golangci-lint incl. staticcheck + gosec, `go test -race`, govulncheck) | ✅ Done: all checks pass locally |
| 1 | `mention`: parser | ✅ Done: 59 table cases + fuzz test (11M runs, no panics), 98% coverage |
| 2 | `config` + `store`: env validation, migrations, repository CRUD | ✅ Done: config 100%, store 85% coverage (temp SQLite files) |
| 3 | `githubhook`: signature check + event → recipients (catch-all + specific handlers + noise rules) | ✅ Done: 22 payload fixtures + GitHub docs signature test vector |
| 4 | `githubapi`: org/team members, username→ID with cache | ✅ Done: fake GitHub API; cross-host pagination refused; stale-cache fallback |
| 5 | `notify` + `discord`: message builder + client | ✅ Done: message builder tests; fake Discord covers 429/5xx/token redaction |
| 6 | `server`: end-to-end GitHub flow | ✅ Done: handler tests + end-to-end boot test (signed webhook against fake GitHub/Discord APIs) |
| 7 | `discordcmd`: Ed25519 check, `/admin` commands, autocomplete, registration | ✅ Done: signed interaction tests, admin/guild/DM checks, autocomplete, registration |
| 8 | Packaging and deploy: systemd unit, host Caddy site, auto-deploy workflow, Docker option, README | ✅ Done: deployed to pingcord.lexerlabs.com; webhook, `/admin` and HTTPS verified |
| 9 | Review: code review + security review, coverage ≥ 80% | ✅ Done: 0 CRITICAL; 2 HIGH, 7 MEDIUM and the cheap LOW findings fixed; govulncheck clean; coverage ≥ 85% per package |

## 14. Setup steps (full version: README + deploy/README.md)

1. **Discord channel webhook:** Channel → Integrations → Webhooks → copy the URL.
2. **Discord application:** Developer Portal → New Application. Copy the App ID and Public
   Key; Bot tab → token. Invite the app with the `applications.commands` + `bot` scopes. Set
   the Interactions Endpoint URL to `https://<domain>/discord/interactions`.
3. **GitHub token:** fine-grained PAT → resource owner = your org →
   Organization permissions → Members: Read-only.
4. **GitHub org webhook:** Org → Settings → Webhooks →
   `https://<domain>/github/webhook`, JSON, secret = `GITHUB_WEBHOOK_SECRET`. Events:
   Issues, Issue comments, Pull requests, Pull request reviews, Pull request review
   comments, plus Discussions / Commit comments if you want them.
5. **Server:** host Caddy, `/etc/pingcord/pingcord.env`, systemd unit, deploy secrets; push to `main` (see `deploy/README.md`).
6. **Onboard devs:** `/admin unlinked`, then `/admin link` for each person.

## 15. Risks and open questions

| Risk | Mitigation |
|---|---|
| The fine-grained PAT expires (≤ 1 year) | Log a warning at startup and when the token is close to expiry; README documents rotation. Later: support a GitHub App for tokens that don't expire |
| Several reviews or comments in a burst cause many pings | Accepted for v1. Later: optional per-user debounce window |
| Inline review comments without a submitted review event | Check against real payloads in phase 3 and adjust rule 5.3.3 if needed |
| An admin's Discord account is compromised | The admin list lives in env (changing it needs server access); every change is announced in the channel |

## 16. Later (not v1)

- Opt-in self-service `/link` with GitHub OAuth proof of ownership
- Per-user preferences (mute event types, quiet hours, DM instead of the channel)
- GitHub App authentication
- Multiple orgs or channels (route by repo)

## 17. Known limitations (accepted after review)

- Indented code blocks and lazy block-quote continuation lines are scanned for mentions.
- No de-duplication by `X-GitHub-Delivery`: a manual Redeliver of a delivery
  that actually succeeded pings again.
- After a GitHub rename, `/admin unignore <new-login>` works only once a payload
  containing that user has been seen (stored login is refreshed from payloads).
- Interactions accept a 5-minute timestamp window without a nonce (replay
  requires breaking TLS).
