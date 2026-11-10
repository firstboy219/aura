# ADR-0055 — Owner reply and adaptive view

Status: **Proposed** (2026-11-10). Owner request: AURA must answer the owner, communicate well, and "make its own
interface" that fits the task. Before this ADR a finished task had a state, events and files, but no answer: the app
showed `COMPLETED` and nothing to read.

## Decision
**1. A reply is the last phase of every owner task.** After completion checks and verification pass, and before the
task becomes `COMPLETED`, the Core calls the worker method `task.reply` (contract `contracts/schemas/task.reply.json`).
The worker returns `{text, view}`. The Core stores it in `task_replies` (migration 0014), emits `OWNER_REPLY`, and the
API returns it as `reply` on a task (`GET /tasks`, `GET /tasks/{id}`).

**2. Direct answers.** When a task needs no tool, no file and no approval, the planner may return a plan with zero
steps. The Core accepts that only when `mayAnswer(task)` holds; the reply is then written from the model's knowledge and
is always labelled `model_knowledge`. A task that requires work never takes this path.

**3. The view is data, never code.** A view is at most 12 blocks of 11 types: text, callout, metrics, kv, table, list,
steps, chart (bar, line, pie), links, code, actions. The model chooses the blocks that fit the task; that is how the
interface adapts.
- The Core (`internal/view`) sanitises every view: unknown types and fields are dropped, sizes are capped, links must
  be http(s), control characters are removed. Clients see only the sanitised form.
- Clients draw blocks with native components: the Web App with text nodes and SVG attributes (no `innerHTML`, CSP
  unchanged), Android with widgets and a canvas, Telegram and the CLI as text.
- An `actions` block only proposes a follow-up request. Pressing it creates a normal new task under the same policy,
  budget and approval rules. A view cannot call a tool, approve anything or widen a grant.

**4. Honesty notes, decided by the Core.** `model_knowledge` (no tool was used), `ungrounded_numbers` (figures in the
reply that do not occur in the task's evidence), `view_trimmed` (the sanitiser removed something), `core_summary` (no
model could write the reply). Clients must show them.

**5. Failure.** If the reply writer fails on a task that did real work, the Core writes a plain summary itself and the
task still completes. If it fails on a direct answer, the task fails: there is nothing else to deliver.

**6. Conversation and self brief.** The planner and the reply writer receive the owner's recent requests with AURA's
replies (same member, and never a private exchange inside a public task), plus a short self brief built by the Core
from real state: version, enabled tools, open limitations. The brief states that AURA is software with an operational
self-model and makes no claim of consciousness (Blueprint, Charter).

**7. Off switches.** `reply.disabled: true` in the config; `reply: "none"` per task. Lab and self-improvement tasks
set it, so evaluation is not affected.

## Alternatives
- Let the model emit HTML or a UI script: rejected. Model output is untrusted (AGENTS rule 6) and would run in the
  owner's session.
- Compose the answer in each client from events: every client would differ, and none could note ungrounded figures.
- Treat the reply as a plan step: it would be subject to step verification and could be skipped by the planner.

## Consequences
- One or two extra model calls per task (fast tier first, then standard), inside the task budget.
- `ungrounded_numbers` is a textual check. It catches invented figures, not wrong reasoning.
- The reply writer needs a usable route. A private task with no non-logging route gets a `core_summary` or fails.

## Evidence (implementation-verified)
- `engine/reply_test.go` (8 tests): direct answer labelled; untraced numbers flagged; writer failure falls back to the
  Core summary; direct answer fails without a reply; no direct answer when work is required; conversation context
  respects the data class; `reply: none`; number grounding.
- `view/view_test.go`: sanitiser. `intelligence/python/tests/test_agent.py`: planner and reply writer.
- Android `ClientTest.parsesReplyAndView`.
