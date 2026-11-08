# 0002 — Long polling by default, webhook as an opt-in

**Status:** accepted

## Context

The bot must answer on a home network behind NAT, often without a public hostname. A webhook needs a public
HTTPS endpoint, a reverse proxy or tunnel, and a secret header; polling needs nothing but outbound TLS.

Telegram has a hard property that shaped the design: `getUpdates` deletes an update once a response was
delivered to the client. A process that dies *after* receiving an update but *before* answering it never gets
that update again. Measured during the project: a restart resumed at offset 920551679, the next update was
920551680, and Telegram re-delivered nothing for 9.5 minutes.

## Decision

Polling is the default transport (`bot.Options`), webhook is opt-in behind `WEBHOOK_ENABLED` and requires a
non-empty secret (the listener refuses to start without one). The processed offset lives in `bot_state`, so a
restart continues where the previous run stopped instead of at zero.

Because Telegram will not replay an update the process already received, correctness does not rely on redelivery.
The question is written to `messages` *before* the model call, with a UNIQUE `(chat_id, telegram_message_id)`;
a startup scan (`bot.ResumeInterruptedQuestions`) finds chats whose newest visible row is an unanswered user
question and answers them, with a persisted exponential backoff and a `RECOVERY_MAX_AGE_HOURS` cap.

## Consequences

- A crash can leave at most one unanswered question per chat, and the next start cleans it up.
- Webhook mode is implemented and tested but not the production path; it must be behind a proxy that forwards
  the same path and the `X-Telegram-Bot-Api-Secret-Token` header.
- The per-chat queue is not a substitute for redelivery: an update that waits longer than the chat budget is
  refused with a "chat is busy" answer and is not acknowledged in `bot_state`, so a restart replays it.
- Revisit if a deployment needs multiple replicas: polling with one process per token is the constraint that
  keeps the design single-writer.
