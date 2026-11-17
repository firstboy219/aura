# ADR-0050 — AUL v1: capability-safe workflows compiled to IR and run in a WebAssembly VM

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)
static checks; AUL executes as WASM under Core grants". It follows the Blueprint §11 pipeline: Source → Lexer → Parser → AST →
type/capability check → IR → policy validation → WASM.

## Decision
- **Language (v1, workflows only).**
  - Header: `workflow <name> version <n>`.
  - Declarations: `requires <tool> domains [...] | paths [...]`, and `input <name>: <type>`.
  - Statements: `let`, `for x in <list> { }`, `if/else`, `call <tool> { args }`, `return`, `fail`.
  - Expressions: JSON values, `+ - * /`, comparisons, `and/or/not`, field and index access, and 13 pure builtins (`len`,
    `join`, `slice`, …).
  - Not in the language: while loops, recursion, user functions, eval, clock, environment, files or network.
  Agent definitions and policies in AUL are later scope. Policies are L4 and stay owner/Claude Code changes.
- **Tools available to AUL:** `web.fetch` (domains), `workspace.read` and `workspace.write` (path prefixes).
  - `published/` cannot be declared, because workflows never ask for approvals.
  - Any tool that needs an A2 approval is refused at run time (`approval_required`).
- **Static checks (compile time):**
  - a call to a tool not in `requires` fails ("undeclared capability");
  - literal URLs and paths must fall inside the declared domains and paths, with traversal refused;
  - call arguments must be exactly the tool's keys;
  - variables are defined before use;
  - only known builtins are allowed;
  - source is at most 64 KiB, nesting at most 16 levels, at most 5000 AST nodes.
  - Malformed input yields diagnostics, never a crash: a fuzz test runs 3000 mutated sources.
- **IR and the WASM VM.**
  - The checked program is lowered to a JSON IR, versioned and sha256-pinned, and stored with the source (`aul_workflows`).
  - It runs in the **AUL VM**, an interpreter written in Go and compiled to WebAssembly (`GOOS=wasip1`). The VM is built
    from this repository and signed in the release manifest like python.wasm (ADR-0037).
  - The Core instantiates the VM under wazero:
    - the VM may import WASI preview 1 only (`sandbox.CheckImports`);
    - no files are mounted, no network is available, no environment is passed;
    - memory is capped (128 MiB);
    - the module closes with the task's context.
  - The program and its inputs go in on stdin. Each tool call comes out on stdout and waits for the Core's answer. That is
    the VM's only way out.
  - Precise claim: the program is not translated to WASM instructions one construct at a time. A checked IR runs inside a
    WASM-isolated VM. A bug in the language runtime stays inside the WASM sandbox.
- **Core authority at run time.** For every call, the Core checks all of these:
  - the tool is declared in the stored program *and* in the task's grant;
  - the arguments have the right keys and types;
  - the tool is A0 (no approval needed);
  - URLs pass both the declaration and the task's domain allowlist (`tools.CheckURL`: SSRF guard, schemes);
  - paths stay inside the declared prefixes.
  The Core also enforces the kill switch, call limits (`aul.max_calls`), operation limits, and size limits on strings,
  lists and messages. Refusals are audited (`CAPABILITY_DENIED`, actor `aul`), and every call is a task event (`AUL_CALL`).
- **Integrity.** Before a run, the stored IR must match its sha256. A tampered row fails with `AUL_INTEGRITY`. The VM itself
  also refuses calls to undeclared tools, even in hand-made IR.
- **Lifecycle.**
  - Registering a workflow is owner-only and audited. Versions only go up; identical content is idempotent; a new version
    supersedes the old.
  - Running is operator-level, through the API or `aul workflow run`. It creates a task with role `workflow` whose grant is
    exactly the declared capabilities.
  - Workflow tasks use no model. They stay out of the self-model, the Ledger and canary comparisons, and their failures map
    to owner action (R6).
- **OD-5 (specification visibility):** customer-only documentation by default. It is never a security boundary.

## Evidence
- `internal/aul/aul_test.go`:
  - capability and static checks (12 cases);
  - fuzzing that never panics;
  - VM semantics and limits (op limit, memory growth, tampered IR);
  - the VM built as wasip1 WASM, run under wazero: tool calls through the host, a refused call, the call limit, the op
    limit inside WASM, and a timeout that closes the module.
- `engine/aul_test.go`:
  - register, run (the file is written, no model use, run recorded);
  - an out-of-scope URL from the inputs is refused and audited;
  - tampered IR fails with `AUL_INTEGRITY`;
  - versioning, disable, unknown inputs.

## Limits
- v1 covers workflows only. Agent and policy definitions, `web.search`, and data and MCP tools are not available yet.
- A workflow interrupted by a Core restart re-runs from the start. Its tools are reads or workspace writes, so a re-run only
  overwrites the same files.
- The VM is about 2–3 MB of WASM. The first compile takes a few seconds and is cached in `l2.cache_dir`.

## Live check (production, 0.10.0-p8, 2026-11-10)
The release manifest verified 460 files, `aulvm.wasm` among them. `example_digest` v1 was registered and run on
https://example.com/ and https://www.iana.org/help/example-domains. Trace: `AUL_STARTED` (program and VM sha256), then three
`AUL_CALL` events (2× web.fetch, 1× workspace.write), then `AUL_COMPLETED {"pages":2}`, about 140 ms after the VM started.
No model was used.
