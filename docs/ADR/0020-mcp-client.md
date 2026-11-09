# ADR-0020 — MCP client adapter

Status: **Proposed** (owner decided P2→P3 without a pause, A-21). Date: 2026-11-09.

## Decision
MCP servers are declared by the owner in config (`mcp.servers[]`: name, command, env allowlist, and per tool: tier,
side-effect class, timeout). Only declared tools are exposed, as `mcp.<server>.<tool>`. The server's own tool list and
descriptions are untrusted metadata and never change tier or side-effect class. Servers run as stdio child processes
of the Core (no secrets in their environment unless the owner lists a key file for that server). Calls go through the
same grant, budget, approval and audit path as built-in tools. Protocol subset: `initialize`,
`notifications/initialized`, `tools/list`, `tools/call`.
