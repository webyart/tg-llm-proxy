# 0003 — Fail fast on any configuration problem, deny by default

**Status:** accepted

## Context

The bot holds a Telegram token, an optional upstream API key and a webhook secret, and it answers whoever is on
its allowlist. A half-configured process is worse than a stopped one: it looks alive, may answer the wrong
people, and hides the row an operator mis-typed behind defaults that "work".

## Decision

- The loader (`config.LoadFrom`) reads a fixed table of rows, collects **every** problem into one list and names
  each row; `main` prints the list and exits with code 1 if it is not empty. There is no "fall back to the
  default and continue" in production — the fallback values inside the loader are the safety net for values that
  cannot be parsed, not a mode of operation.
- A row that left the table (`ALLOWED_USER_IDS`, the `LLM_*` family) is refused by name when it carries a value,
  so a stale `.env` cannot pretend to configure the bot.
- An empty `TELEGRAM_ALLOWED_USERS` closes the bot (deny by default): only `/start` and `/whoami` answer, so a
  new user can learn their id and be added.
- Secrets travel only in the process ENV; `.env` is excluded from git and from the Docker context, and the
  startup summary prints `token_set`/`token_length`, never the value.
- Addresses that could leak credentials — userinfo, query, fragment in `STRATA_BASE_URL` — are rejected, and a
  non-local base must be HTTPS.

## Consequences

- Every start is either fully configured or refused with the reason; there is no "it started, but…" state.
- A deployment with a broken `.env` restarts forever under `restart: unless-stopped`. The monitor script alerts
  on the crash loop; the alternative (`on-failure:3`) would give up on transient network failures instead.
- Tests construct configurations through the same loader, so a new required row or a changed bound breaks the
  tests rather than production.
