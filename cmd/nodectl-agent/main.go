// 路径: cmd/nodectl-agent/main.go
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"runtime/debug"

	"nodectl/internal/agent"
)

func main() {
	configPath := flag.String("config", agent.DefaultConfigPath, "配置文件路径")
	showVersion := flag.Bool("version", false, "显示版本号")
	flag.Parse()

	if *showVersion {
		fmt.Printf("nodectl-agent %s (commit=%s, built=%s)\n",
			agent.AgentVersion, agent.GitCommit, agent.BuildTime)
		os.Exit(0)
	}

	log.SetFlags(log.Ldate | log.Ltime | log.Lmsgprefix)
	log.SetPrefix("")

	// 设置 GOMEMLIMIT（如果环境变量未覆盖，则使用 16 MiB 软上限）
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(16 << 20) // 16 MiB
	}

	// 初始化自动更新器 + 崩溃循环检测
	updater, err := agent.NewUpdater()
	if err != nil {
		log.Printf("[Agent] 初始化更新器失败 (将禁用自动更新): %v", err)
	}
	if updater != nil {
		if needRestart := updater.RecordStartup(); needRestart {
			// 崩溃次数已达上限，已回滚到旧版本，需要用旧二进制重新启动
			log.Printf("[Agent] 已回滚到旧版本，请通过 systemd 重启")
			os.Exit(1)
		}
	}

	// 1. 加载配置
	cfg, err := agent.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("[Agent] 加载配置失败: %v", err)
	}

	// 2. 创建并启动运行时
	rt := agent.NewRuntime(cfg, updater)
	if err := rt.Run(); err != nil {
		log.Fatalf("[Agent] 运行时异常退出: %v", err)
	}
}
