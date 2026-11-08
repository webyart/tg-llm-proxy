# 0006 — The container restarts `unless-stopped`, and a crash loop is the monitor's job

**Status:** accepted (2026-11-08; the review proposed `on-failure:3`, this record explains why not)

## Context

Configuration problems are fatal at start (`ADR 0003`): a broken `.env` makes the process exit with code 1 in
under a second. Under `restart: unless-stopped` that becomes a restart loop — Docker backs off exponentially up
to a minute, but the container never stops trying. The review flagged this as a way to hide a persistent
failure behind a permanently restarting container.

The alternative, `restart: on-failure:3`, stops trying after three failures. That is right for a batch job and
wrong for a chat bot: the same policy applies to a transient network outage or a Telegram route hiccup, and
after the third failure the bot would stay silent until someone notices and starts it by hand.

## Decision

Keep `restart: unless-stopped`. A persistent failure is detected by the existing monitor
(`scripts/watch_container.ps1`, every 5 minutes): it names the service in a failure state, sends one Telegram
alert per distinct failure (repeat suppressed for 6 hours) and does not restart the container itself, so the
state an operator inspects is the state that failed.

The startup path is what makes the loop cheap and visible: a fatal configuration error exits before any
network call, prints the exact row that is wrong, and costs a fraction of a second. The loop is therefore not a
"silent" failure — the logs and the alert both say what is broken.

## Consequences

- A misconfigured `.env` shows up as an alert within minutes, with the container in a restart backoff.
- A transient outage recovers by itself, which is what a personal bot needs at 3 a.m.
- If a deployment ever needs a hard stop after N failures, it is a compose override for that deployment, not a
  repository default — and the monitor alert stays the primary signal.
