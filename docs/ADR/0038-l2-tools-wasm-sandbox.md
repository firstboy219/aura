# ADR-0038 — L2 tools in a WASM sandbox (remedy R3)

Status: **Proposed** (P4-M4, 2026-11-10). Implements Blueprint 8A.1 L2 ("new executable tool with manifest/tests/capabilities;
isolated WASM or equivalent sandbox; tests, static capability check, sandbox benchmark; new capabilities require owner approval")
and D.1 P4 (RS-6; undeclared WASM host calls fail; the Limitation Ledger resolves a missing-tool limitation through R3).

## Decision
- **Sandbox:** wazero v1.9.0, a pure-Go WebAssembly runtime (Apache-2.0, no cgo). It runs CPython 3.12 compiled to WASI, with the
  standard library embedded, from vmware-labs/webassembly-language-runtimes (CPython is PSF-licensed; the build project is
  Apache-2.0). The module is pinned by sha256 in code, in the Dockerfile and in the signed release.
  - Only WASI preview 1 host functions are provided. A module importing anything else fails at compile (`CheckImports`) and at
    instantiation.
  - WASI preview 1 has no socket creation and no directory is mounted, so there is no network and no host file.
  - The environment holds only two fixed variables. Each run has a 128 MiB memory limit, a 10 s wall-time limit enforced by context
    close, and a 256 KiB output limit.
- **An L2 tool** is a pure Python function `run(input) -> JSON`. It has no capabilities at all. The Core passes its input and, on
  request, the text of a workspace file of the same task. Its side-effect class is `pure` and its tier A0.
- **Gates**, all run inside the sandbox:
  1. Name, schema and size checks.
  2. AST static check: only an allowlisted set of pure standard-library modules; no `exec`, `eval`, `compile`, `__import__`,
     `open`, `getattr`/`setattr`, `globals`; no dunder attributes; a top-level `run(input)` must exist.
  3. Every test runs three times. The output must equal the stated expected output, numbers within 1e-9, and be deterministic.
  4. Benchmark: p50 and max wall time.

  A failing draft gets one repair round with the exact problems. If it fails again, the remedy is escalated to the owner.
- **Owner approval bound to the source hash:** sha256 over name, input schema and source. Before approval the tool is not in the
  manifest, and a task cannot be granted it.
  - The owner reviews the source, tests and report with `aura tools show <id>` (or `GET /api/v1/tools/{id}`).
  - `aura tools approve <id>` sends the exact hash it showed. A different hash is refused.
  - Approval supersedes the previous version of the same tool. `reject` and `disable` exist, and every decision is audited.
- **Closed loop:**
  1. An owner-reported `capability_gap` limitation, or a diagnosed `missing_tool`, is routed to R3.
  2. `task.build_tool` drafts the tool, inside the improvement budget.
  3. After approval, the limitation's failed tasks run again with the tool granted and their attachments copied, alongside a
     regression sample.
  4. The remedy is promoted on measured improvement or rolled back. Rollback disables the tool.
- **Rule 5 ("no exec of model output"):** model-written code runs only inside this sandbox, which is what Blueprint 8A.1 L2 and
  §8.8 allow. The trusted harness `exec`s it there, never in the Core or the worker.
- **Concurrency:** the tool manifest is now read and written under a lock, because L2 tools are registered and removed at
  runtime.

## Evidence
- `internal/sandbox` tests: JSON in and out; no host files (`/etc/passwd`, the database, secrets, `/proc`) and no network; infinite
  loop, memory bomb and output flood are stopped; a module importing `env.f` is refused at compile and at instantiation; an
  unpinned module is refused.
- `engine/l2_test.go`:
  - `TestRS6L2ToolRemedyEndToEnd`: a CSV task fails without the tool, and the tool cannot be granted before approval. The
    owner-reported capability gap goes to R3. The first draft imports `os`, is refused, and is repaired. The repaired draft passes
    tests and benchmark and waits for approval. A wrong hash is refused and the right hash registers the tool. The re-test uses
    `l2.csv_column_stats` and gets rows 4 and sum 4082345.85. The limitation is resolved.
  - `TestL2Gates`: wrong result, `import os`, `eval`, dunder escape, `open`, missing `run` and non-determinism are each refused.

## Limits
- Interpreter start costs about 0.4 s per call. The first compile of the module takes several seconds; it is cached in
  `l2.cache_dir`.
- L2 tools compute only. Tools that need network or files are R6 (owner) decisions, never granted automatically.
- The static check is defence in depth. The sandbox is the boundary.
