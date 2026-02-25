// 路径: internal/agent/agentversion.go
// Agent 独立版本变量，与主程序 internal/version 解耦
// 通过 -ldflags 注入:
//
//	-X nodectl/internal/agent.AgentVersion=...
//	-X nodectl/internal/agent.GitCommit=...
//	-X nodectl/internal/agent.BuildTime=...
package agent

var (
	// AgentVersion agent 语义化版本号（由 CI 注入）
	AgentVersion = "dev"
	// GitCommit 构建时的 git commit SHA（短）
	GitCommit = "unknown"
	// BuildTime 构建时间 (RFC3339)
	BuildTime = "unknown"
)
