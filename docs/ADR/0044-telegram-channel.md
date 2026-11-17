# ADR-0044 — First messaging channel: Telegram bot

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)
owner's instruction to continue to the last phase is recorded as waiving the P5a acceptance gate (ASSUMPTIONS A-28).

## Decision
- **Bot:** a Telegram bot driven from the Core over the official Bot API (`getUpdates` long polling). The connection is outbound
  only; no port is opened and no webhook endpoint exists.
  - The token is a write-only admin credential (`vault:connector.telegram`) and is never logged; error messages never contain the
    URL.
  - Only private chats from `allowed_user_ids` are served. Other users are ignored and audited (`CHANNEL_REFUSED`), and their id
    is logged so the owner can find their own.
- **Commands:** a plain message creates a task, using the default tools and domains, private data class. Also: `/tasks`,
  `/status <id>`, `/answer <id> <text>`, and replying to AURA's question message. `/cancel <id>`. Every command is audited.
- **Approvals are never given in chat** (13A.7 rule 4). `/approve` and `/reject` answer with the Web App link, where step-up
  applies.
- **Notifications:** questions, approvals needed, and completed, failed or budget-exceeded tasks are relayed once each
  (`channel_deliveries`, OL-11). The text is plain, with no markup parsing, and model output is truncated.
- **Updates** are acknowledged before handling, so a crash can drop an update but can never run it twice.
- **Continuity:** tasks created in Telegram are the same tasks as in the Web App, API and CLI (same IDs, timeline and
  corrections). There is no duplicate root task.

## Evidence
`app/telegram_test.go`, against a fake Bot API:
- a stranger's message creates nothing and is audited;
- the owner's message creates a task;
- `/approve` is refused with the Web App link;
- updates are not handled twice;
- a question is relayed exactly once;
- a reply to it answers the task.

## Limits
- Not yet run against the real Telegram API: the owner has not created a bot token yet.
- One chat type (private) only, and no voice messages.
