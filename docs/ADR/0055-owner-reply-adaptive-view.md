# ADR-0055 — Owner reply and adaptive view

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)
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

## Addendum (2026-11-10): a failed task also answers
The first sandbox run showed that a failed task ended in silence: the owner saw a state and a reason code.
- Before a root task moves to FAILED or BUDGET_EXCEEDED the Core stores a reply labelled with the note `task_failed`
  (detail: canonical reason code). The state does not change: COMPLETED still means verified, and a failure reply is
  never a result.
- Failures while working (tool, verification, invalid plan or output, replan limit, blocked site) are explained by the
  reply writer in failure mode (`task.reply` with `failure: {code, detail}`): what went wrong in plain words, what was
  found, an optional answer from model knowledge that must be labelled unverified, and one next step. The failure
  detail is given to the model as untrusted data. Basis `model_knowledge`.
- Budget, wall time, provider, policy and crash failures are stated by the Core in fixed wording (basis `summary`),
  with a "try again" action. No model speaks for a policy decision, and none is available when routes or budget ran out.
- The failure reply is best effort (90 s limit) and can never fail or delay the transition beyond that.
- Telegram appends the explanation to the failure notice. Web and Android show the note with the reason code.
- Related fix: a step failure reported by the worker as an RPC error (for example "no suitable URL in the search
  results") was not treated as replannable, although the worker raises it for exactly that purpose. It now leads to
  a replan. Two tests asserted FAILED for a URL without provenance that the worker itself refused; they now assert
  what the rule protects (the step failed and the URL was never fetched), because the planner may finish the task
  with another source. The Core-side denial test (`argument_without_provenance`) is unchanged.
- Conversation context marks the most recent exchange explicitly; a weak model took the oldest one for "what I just
  asked" in the sandbox.
- Evidence: `reply_test.go` `TestDirectAnswerFailsWithCoreExplanation`, `TestFailedTaskIsExplainedToTheOwner`,
  `TestNoDirectAnswerWhenWorkIsRequired`; `test_agent.py` `test_failure_reply_explains_and_never_claims_done`,
  `test_conversation_marks_the_most_recent_exchange`; sandbox suite S10, S11.

## Addendum 2026-11-11: failures name the provider's own answer; conversations are not parked

Found in production: every owner message failed with PROVIDER_UNAVAILABLE and the reply blamed the private-route
rule, even for public tasks. The real cause was the provider rejecting the key (HTTP 401) and a second provider out
of quota. Greetings ("tes halo") were turned into clarification questions and waited in WAITING_INPUT.

- The gateway remembers each provider's last definite refusal (401/403 key rejected, 402 no credit, 429 quota) until
  that provider answers again (`gateway/issues.go`, `ProviderIssues`). A PROVIDER_UNAVAILABLE reply states those
  refusals, what the owner can do (the `aura secrets import <name> --stdin` command when the key is in the vault) or
  how long until a quota-limited route is tried again; the private-route sentence appears only for private tasks.
- When a provider starts refusing the key or credit, the owner gets one notification (`PROVIDER_REFUSED`, audited).
  Quota refusals are not notified: waiting fixes them.
- A planner question about a conversational request (nothing a tool must produce) no longer parks the task: the
  reply is written directly and may itself ask for the one missing detail; the owner's next message continues the
  conversation. Requests that need tools still use WAITING_INPUT. The planner is told not to answer greetings with a
  question and to ask in the objective's language.
- Evidence: `gateway_test.go` `TestProviderRefusalIsRememberedUntilItAnswers`, `TestQuotaIssueIsReportedWithoutNotification`;
  `reply_test.go` `TestProviderRefusalIsNamedInTheFailureReply`, `TestConversationalQuestionIsAskedInTheReplyNotParked`.
