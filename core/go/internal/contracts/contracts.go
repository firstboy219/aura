// Package contracts locates the shared Go–Python contract files (Blueprint §9).
//
// Phase 0 only checks that the schemas exist and are well-formed; request
// validation against them arrives with the worker supervisor in Phase 1.
package contracts

// Methods are the Phase 1 JSON-RPC methods; each has contracts/schemas/<method>.json.
var Methods = []string{
	"task.plan", "task.review", "task.step", "task.replan", "task.verify", "task.reflect", "task.reply", "worker.health", "worker.shutdown",
	"llm.complete", "tool.invoke", "task.checkpoint", "approval.request",
}
