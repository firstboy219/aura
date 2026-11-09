# Benchmark runners

- `rs1.sh [runs]` — RS-1 acceptance against the deployed `aura-core` container with the real provider (opt-in;
  spends OpenRouter credit). Creates the task through the CLI, waits for a terminal state, checks the summary file
  (three source links, each summary ≤ 150 words) and prints PASS/FAIL per run; artifacts go to a temp directory.
- Deterministic RS-1 (fake provider, fixture server) runs in CI: `go test ./internal/engine -run TestRS1FakeProvider`.
