// 路径: internal/agent/runtime.go
package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Runtime Agent 运行时：调度采集与上报，管理信号与生命周期
type Runtime struct {
	cfg       *Config
	collector *Collector
	reporter  *Reporter
	state     *State
	updater   *Updater
	logDedup  *LogDedup
	cancel    context.CancelFunc
	// 启动时从 state 加载的基线累计值（运行期间不变，避免双重计数）
	startupRX int64
	startupTX int64
}

// NewRuntime 创建运行时实例
func NewRuntime(cfg *Config, updater *Updater) *Runtime {
	return &Runtime{
		cfg:       cfg,
		collector: NewCollector(cfg.Interface),
		reporter:  NewReporter(cfg),
		state:     NewState(""),
		updater:   updater,
		logDedup:  NewLogDedup(),
	}
}

// Run 启动 Agent 主循环（阻塞直到收到退出信号）
func (rt *Runtime) Run() error {
	// 加载持久化状态
	if err := rt.state.Load(); err != nil {
		log.Printf("[Agent] 加载状态文件失败 (将使用零值): %v", err)
	}

	// 捕获启动基线（跨重启的历史累计值），运行期间不再变化
	rt.startupRX, rt.startupTX = rt.state.GetAccumulated()

	// 初始化采集器
	if err := rt.collector.Init(); err != nil {
		return err
	}

	// 注册命令处理器
	rt.reporter.SetCommandHandler(rt.handleCommand)

	// 创建主 context
	ctx, cancel := context.WithCancel(context.Background())
	rt.cancel = cancel
	defer cancel()

	// 首次连接 (无限重试直到成功或被中断)
	for {
		if err := rt.reporter.Connect(ctx); err != nil {
			log.Printf("[Agent] 首次连接失败: %v", err)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := rt.reporter.ReconnectWithBackoff(ctx); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				continue
			}
		}
		break
	}

	// 启动信号监听
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	// 首次 WS 连接成功，标记健康（清除崩溃计数）
	if rt.updater != nil {
		rt.updater.MarkHealthy()
	}

	// 启动自动更新检查循环（后台 goroutine）
	if rt.updater != nil {
		go rt.updater.Run(ctx)
	}

	// 启动主循环
	pushTicker := time.NewTicker(time.Duration(rt.cfg.WSPushIntervalSec) * time.Second)
	snapshotTicker := time.NewTicker(time.Duration(rt.cfg.SnapshotIntervalSec) * time.Second)
	stateSaveTicker := time.NewTicker(60 * time.Second) // 每分钟持久化状态
	defer pushTicker.Stop()
	defer snapshotTicker.Stop()
	defer stateSaveTicker.Stop()

	// 启动日志（仅输出一条）
	log.Printf("[Agent] nodectl-agent %s 已启动 (install_id=%s, iface=%s, push=%ds, snapshot=%ds)",
		AgentVersion, rt.cfg.InstallID, rt.collector.GetInterface(),
		rt.cfg.WSPushIntervalSec, rt.cfg.SnapshotIntervalSec)

	// 是否到了快照周期
	snapshotDue := false

	for {
		select {
		case <-ctx.Done():
			rt.shutdown()
			return nil

		case sig := <-sigCh:
			switch sig {
			case syscall.SIGHUP:
				rt.reloadConfig()
			case syscall.SIGINT, syscall.SIGTERM:
				log.Printf("[Agent] 收到 %v 信号，准备退出...", sig)
				cancel()
			}

		case <-snapshotTicker.C:
			snapshotDue = true

		case <-pushTicker.C:
			// 采集一次数据
			if err := rt.collector.Sample(); err != nil {
				rt.logDedup.LogOrSuppress("collector:sample", "[Agent] 采集失败: %v", err)
				continue
			}

			// 检查月度重置
			if rt.state.CheckMonthlyReset(rt.cfg.ResetDay) {
				log.Printf("[Agent] 执行月度流量重置 (reset_day=%d)", rt.cfg.ResetDay)
				rt.collector.ResetAccumulated()
				rt.startupRX = 0
				rt.startupTX = 0
				if err := rt.state.Save(); err != nil {
					log.Printf("[Agent] 保存重置状态失败: %v", err)
				}
			}

			rxRate, txRate := rt.collector.GetRates()

			if snapshotDue {
				// 发送带累计快照的消息
				accumRX, accumTX := rt.collector.GetAccumulated()
				// 加上启动时的历史基线（固定值，避免双重计数）
				totalRX := accumRX + rt.startupRX
				totalTX := accumTX + rt.startupTX

				err := rt.reporter.SendSnapshotMessage(ctx, rxRate, txRate, totalRX, totalTX)
				if err != nil {
					rt.logDedup.LogOrSuppress("reporter:snapshot", "[Agent] 快照上报失败: %v", err)
					rt.handleDisconnect(ctx)
				} else {
					rt.state.UpdateOnReport(totalRX, totalTX)
					snapshotDue = false
				}
			} else {
				// 发送仅速率消息
				err := rt.reporter.SendRateMessage(ctx, rxRate, txRate)
				if err != nil {
					rt.logDedup.LogOrSuppress("reporter:rate", "[Agent] 速率上报失败: %v", err)
					rt.handleDisconnect(ctx)
				}
			}

		case <-stateSaveTicker.C:
			if err := rt.state.Save(); err != nil {
				rt.logDedup.LogOrSuppress("state:save", "[Agent] 持久化状态失败: %v", err)
			}
		}
	}
}

// handleDisconnect 处理断线重连
func (rt *Runtime) handleDisconnect(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	log.Printf("[Agent] 检测到连接断开，启动重连...")
	for {
		if ctx.Err() != nil {
			return
		}
		if err := rt.reporter.ReconnectWithBackoff(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			rt.logDedup.LogOrSuppress("reporter:reconnect", "[Agent] 重连失败: %v", err)
			continue
		}
		log.Printf("[Agent] 重连成功")
		return
	}
}

// reloadConfig 重新加载配置文件 (SIGHUP)
// 注意：仅更新可动态变更的字段，网卡变更需要重启
func (rt *Runtime) reloadConfig() {
	newCfg, err := LoadConfig("")
	if err != nil {
		log.Printf("[Agent] 重新加载配置失败: %v", err)
		return
	}

	// 仅应用可安全动态变更的字段
	rt.cfg.WSPushIntervalSec = newCfg.WSPushIntervalSec
	rt.cfg.SnapshotIntervalSec = newCfg.SnapshotIntervalSec
	rt.cfg.ResetDay = newCfg.ResetDay
	rt.cfg.LogLevel = newCfg.LogLevel

	// 网卡变更需要重启，在此仅记录警告
	if newCfg.Interface != rt.cfg.Interface {
		log.Printf("[Agent] 网卡配置变更 (%s -> %s) 需要重启 agent 才能生效", rt.cfg.Interface, newCfg.Interface)
	}

	log.Printf("[Agent] 配置已重新加载")
}

// shutdown 优雅退出
// 顺序：collector（释放 FD）→ state（持久化）→ reporter（关闭 WS）
func (rt *Runtime) shutdown() {
	log.Printf("[Agent] 正在优雅退出...")

	// 0. 输出日志去重器的累计信息
	rt.logDedup.Flush()

	// 1. 释放采集器常驻 FD
	if err := rt.collector.Close(); err != nil {
		log.Printf("[Agent] 关闭采集器失败: %v", err)
	}

	// 2. 持久化状态
	if err := rt.state.Save(); err != nil {
		log.Printf("[Agent] 退出前保存状态失败: %v", err)
	}

	// 3. 关闭 WebSocket 连接
	rt.reporter.Close()

	log.Printf("[Agent] 已退出")
}

// handleCommand 处理后端下发的命令
func (rt *Runtime) handleCommand(cmd ServerCommand, reply func(CommandResult)) {
	// 先回复 accepted
	reply(CommandResult{
		Type:   "accepted",
		Status: "ok",
		Stage:  "命令已接收",
	})

	switch cmd.Action {
	case "reset-links":
		rt.executeResetLinks(cmd, reply)
	case "reinstall-singbox":
		rt.executeReinstallSingbox(cmd, reply)
	default:
		reply(CommandResult{
			Type:    "result",
			Status:  "error",
			Message: fmt.Sprintf("未知命令: %s", cmd.Action),
		})
	}
}

// deriveScriptURL 从 ws_url 推导安装脚本 URL
func (rt *Runtime) deriveScriptURL() string {
	// ws_url 形如 wss://domain:port/api/callback/traffic/ws
	panelURL := rt.cfg.WSURL
	panelURL = strings.Replace(panelURL, "wss://", "https://", 1)
	panelURL = strings.Replace(panelURL, "ws://", "http://", 1)
	if idx := strings.Index(panelURL, "/api/"); idx > 0 {
		panelURL = panelURL[:idx]
	}
	return fmt.Sprintf("%s/api/public/install-script?id=%s", panelURL, rt.cfg.InstallID)
}

// executeResetLinks 重置节点链接
// 后端下发 payload 中包含 {"protocols": ["ss", "hy2", ...]}，作为安装脚本的 CLI 参数
func (rt *Runtime) executeResetLinks(cmd ServerCommand, reply func(CommandResult)) {
	var payload struct {
		Protocols []string `json:"protocols"`
	}
	if len(cmd.Payload) > 0 {
		json.Unmarshal(cmd.Payload, &payload)
	}
	if len(payload.Protocols) == 0 {
		reply(CommandResult{Type: "result", Status: "error", Message: "未收到协议列表，无法重置"})
		return
	}

	scriptURL := rt.deriveScriptURL()
	protoArgs := strings.Join(payload.Protocols, " ")
	shellCmd := fmt.Sprintf(`export SKIP_AGENT_INSTALL=1; curl -fsSL "%s" | bash -s -- %s`, scriptURL, protoArgs)

	rt.execStreamingScript(shellCmd, "重置链接", reply)
}

// executeReinstallSingbox 重新安装 sing-box（复用安装脚本，同样从 payload 读取协议）
func (rt *Runtime) executeReinstallSingbox(cmd ServerCommand, reply func(CommandResult)) {
	var payload struct {
		Protocols []string `json:"protocols"`
	}
	if len(cmd.Payload) > 0 {
		json.Unmarshal(cmd.Payload, &payload)
	}
	if len(payload.Protocols) == 0 {
		reply(CommandResult{Type: "result", Status: "error", Message: "未收到协议列表，无法重新安装"})
		return
	}

	scriptURL := rt.deriveScriptURL()
	protoArgs := strings.Join(payload.Protocols, " ")
	shellCmd := fmt.Sprintf(`export SKIP_AGENT_INSTALL=1; curl -fsSL "%s" | bash -s -- %s`, scriptURL, protoArgs)

	rt.execStreamingScript(shellCmd, "重新安装", reply)
}

// execStreamingScript 执行 shell 命令并逐行流式回传输出
// 每行作为一个 progress 消息发送，最后发送 result
// 使用 sync.WaitGroup 确保 scanner goroutine 读完所有输出后再发送最终结果
func (rt *Runtime) execStreamingScript(shellCmd string, label string, reply func(CommandResult)) {
	reply(CommandResult{Type: "progress", Stage: fmt.Sprintf("正在执行%s脚本...", label)})
	log.Printf("[Agent] 执行%s: %s", label, shellCmd)

	cmd := exec.Command("/bin/sh", "-c", shellCmd)

	// 合并 stdout+stderr 到一个管道（有意为之，便于流式回传完整输出）
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		reply(CommandResult{Type: "result", Status: "error", Message: fmt.Sprintf("启动脚本失败: %v", err)})
		return
	}

	// 使用 WaitGroup 等待 scanner goroutine 完成，替代 time.Sleep hack
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 64*1024), 256*1024)
		for scanner.Scan() {
			line := scanner.Text()
			reply(CommandResult{Type: "progress", Stage: line})
		}
	}()

	// 等待脚本执行结束
	err := cmd.Wait()
	pw.Close()

	// 等待 scanner goroutine 读完所有剩余输出
	wg.Wait()

	if err != nil {
		log.Printf("[Agent] %s脚本退出: %v", label, err)
		reply(CommandResult{Type: "result", Status: "error", Message: fmt.Sprintf("脚本执行完毕但退出码非零: %v", err)})
	} else {
		reply(CommandResult{Type: "result", Status: "ok", Message: fmt.Sprintf("%s完成", label)})
	}
}
