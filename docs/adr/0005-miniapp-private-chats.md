# 0005 — The Mini App is a private-chat companion

**Status:** accepted (2026-11-08)

## Context

The Mini App is a companion web client for the bot: status, history, documents, links, tasks, reminders, usage
and settings. Its session is scoped to a **chat**, because that is the bot's own tenancy model — RAG documents,
reminders, tasks and history all belong to a chat, not to a user. In a private Telegram chat the chat id and the
user id are the same value; in a group they are not.

The audit found that a group ticket would hand one allowlisted member a session over a chat shared with others:
history, documents, links and reminders of everybody in that chat, including the ability to delete them.

## Decision

The Mini App is available only in private chats.

- `authenticator.NewLaunchTicket` refuses a ticket whose `chatID != userID` (`ErrPrivateOnly`).
- `Dispatcher.miniAppButton` refuses the same case (`ErrMiniAppPrivateOnly`), and the "More" menu does not offer
  the button in a group.
- A direct launch without a ticket always resolves to `ChatID = UserID` from the signed `user.id`, so it cannot
  create a group scope either.
- Authentication stays as it was: `initData` HMAC with a 5-minute `auth_date` window, a one-time 128-bit launch
  ticket bound to chat+user with a 2-minute TTL, the ticket travelling in the URL fragment, a 15-minute bearer
  session, and a per-user session-exchange budget (6/min) so one account cannot fill the global session table.

## Consequences

- A group chat has no Mini App; the bot's inline menus remain the interface there.
- If a per-user group experience is ever wanted, it is a bigger change: the scope becomes (chat, user), and
  every read path — history, documents, links, reminders, tasks — must filter by the user as well. That would
  also diverge from the bot, where those collections are chat-level.
