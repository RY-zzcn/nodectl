// 路径: internal/agent/runtime.go
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// Runtime Agent 运行时：调度采集与上报，管理信号与生命周期
type Runtime struct {
	cfg       *Config
	collector *Collector
	reporter  *Reporter
	state     *State
	cancel    context.CancelFunc
	// 启动时从 state 加载的基线累计值（运行期间不变，避免双重计数）
	startupRX int64
	startupTX int64
}

// NewRuntime 创建运行时实例
func NewRuntime(cfg *Config) *Runtime {
	return &Runtime{
		cfg:       cfg,
		collector: NewCollector(cfg.Interface),
		reporter:  NewReporter(cfg),
		state:     NewState(""),
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
	log.Printf("[Agent] 历史累计基线 RX=%d TX=%d", rt.startupRX, rt.startupTX)

	// 初始化采集器
	if err := rt.collector.Init(); err != nil {
		return err
	}
	log.Printf("[Agent] 采集器已初始化，网卡: %s", rt.collector.GetInterface())

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

	// 启动主循环
	pushTicker := time.NewTicker(time.Duration(rt.cfg.WSPushIntervalSec) * time.Second)
	snapshotTicker := time.NewTicker(time.Duration(rt.cfg.SnapshotIntervalSec) * time.Second)
	stateSaveTicker := time.NewTicker(60 * time.Second) // 每分钟持久化状态
	defer pushTicker.Stop()
	defer snapshotTicker.Stop()
	defer stateSaveTicker.Stop()

	// 是否到了快照周期
	snapshotDue := false

	log.Printf("[Agent] 主循环已启动 (速率推送 %ds, 快照 %ds)",
		rt.cfg.WSPushIntervalSec, rt.cfg.SnapshotIntervalSec)

	for {
		select {
		case <-ctx.Done():
			rt.shutdown()
			return nil

		case sig := <-sigCh:
			switch sig {
			case syscall.SIGHUP:
				log.Printf("[Agent] 收到 SIGHUP，重新加载配置...")
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
				log.Printf("[Agent] 采集失败: %v", err)
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
					log.Printf("[Agent] 快照上报失败: %v", err)
					rt.handleDisconnect(ctx)
				} else {
					rt.state.UpdateOnReport(totalRX, totalTX)
					snapshotDue = false
				}
			} else {
				// 发送仅速率消息
				err := rt.reporter.SendRateMessage(ctx, rxRate, txRate)
				if err != nil {
					log.Printf("[Agent] 速率上报失败: %v", err)
					rt.handleDisconnect(ctx)
				}
			}

		case <-stateSaveTicker.C:
			if err := rt.state.Save(); err != nil {
				log.Printf("[Agent] 持久化状态失败: %v", err)
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
			log.Printf("[Agent] 重连失败: %v", err)
			continue
		}
		log.Printf("[Agent] 重连成功")
		return
	}
}

// reloadConfig 重新加载配置文件 (SIGHUP)
func (rt *Runtime) reloadConfig() {
	newCfg, err := LoadConfig("")
	if err != nil {
		log.Printf("[Agent] 重新加载配置失败: %v", err)
		return
	}
	rt.cfg = newCfg
	log.Printf("[Agent] 配置已重新加载")
}

// shutdown 优雅退出
func (rt *Runtime) shutdown() {
	log.Printf("[Agent] 正在优雅退出...")

	// 保存状态
	if err := rt.state.Save(); err != nil {
		log.Printf("[Agent] 退出前保存状态失败: %v", err)
	}

	// 关闭 WebSocket 连接
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
	reply(CommandResult{
		Type:  "progress",
		Stage: "正在重新执行安装脚本...",
	})

	// 解析 payload 中的协议列表
	var payload struct {
		Protocols []string `json:"protocols"`
	}
	if len(cmd.Payload) > 0 {
		if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
			log.Printf("[Agent] 解析 payload 失败: %v", err)
		}
	}
	if len(payload.Protocols) == 0 {
		reply(CommandResult{
			Type:    "result",
			Status:  "error",
			Message: "未收到协议列表，无法重置",
		})
		return
	}

	scriptURL := rt.deriveScriptURL()
	protoArgs := strings.Join(payload.Protocols, " ")

	// curl 下载脚本 | bash -s -- ss hy2 vless ...
	shellCmd := fmt.Sprintf(`curl -fsSL "%s" | bash -s -- %s`, scriptURL, protoArgs)
	log.Printf("[Agent] 执行重置链接: %s", shellCmd)

	out, err := exec.Command("/bin/sh", "-c", shellCmd).CombinedOutput()
	if err != nil {
		reply(CommandResult{
			Type:    "result",
			Status:  "error",
			Message: fmt.Sprintf("重置链接失败: %v, 输出: %s", err, string(out)),
		})
		return
	}

	reply(CommandResult{
		Type:    "result",
		Status:  "ok",
		Message: fmt.Sprintf("链接已重置\n%s", string(out)),
	})
}

// executeReinstallSingbox 重新安装 sing-box（复用安装脚本，同样从 payload 读取协议）
func (rt *Runtime) executeReinstallSingbox(cmd ServerCommand, reply func(CommandResult)) {
	reply(CommandResult{
		Type:  "progress",
		Stage: "正在重新安装 sing-box...",
	})

	var payload struct {
		Protocols []string `json:"protocols"`
	}
	if len(cmd.Payload) > 0 {
		if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
			log.Printf("[Agent] 解析 payload 失败: %v", err)
		}
	}
	if len(payload.Protocols) == 0 {
		reply(CommandResult{
			Type:    "result",
			Status:  "error",
			Message: "未收到协议列表，无法重新安装",
		})
		return
	}

	scriptURL := rt.deriveScriptURL()
	protoArgs := strings.Join(payload.Protocols, " ")

	shellCmd := fmt.Sprintf(`curl -fsSL "%s" | bash -s -- %s`, scriptURL, protoArgs)
	log.Printf("[Agent] 执行重装: %s", shellCmd)

	out, err := exec.Command("/bin/sh", "-c", shellCmd).CombinedOutput()
	if err != nil {
		reply(CommandResult{
			Type:    "result",
			Status:  "error",
			Message: fmt.Sprintf("重新安装失败: %v, 输出: %s", err, string(out)),
		})
		return
	}

	reply(CommandResult{
		Type:    "result",
		Status:  "ok",
		Message: "sing-box 已重新安装",
	})
}
