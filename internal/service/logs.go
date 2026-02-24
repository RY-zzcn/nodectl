package service

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type RecentLogEntry struct {
	Time      string `json:"time"`
	Level     string `json:"level"`
	LevelCN   string `json:"level_cn"`
	Source    string `json:"source"`
	Message   string `json:"message"`
	MessageCN string `json:"message_cn"`
	Operation string `json:"operation"`
	Raw       string `json:"raw"`
}

var (
	logTimeReg   = regexp.MustCompile(`time="([^"]+)"`)
	logLevelReg  = regexp.MustCompile(`\blevel=([^\s]+)`)
	logSourceReg = regexp.MustCompile(`\bsource=([^\s]+)`)
	logMsgReg    = regexp.MustCompile(`\bmsg=("[^"]*"|[^\s]+)`)
)

// GetRecentLogs 读取并解析最近日志，按时间倒序返回。
func GetRecentLogs(limit int) ([]RecentLogEntry, error) {
	if limit <= 0 {
		limit = 120
	}
	if limit > 300 {
		limit = 300
	}

	lines, err := readRecentLogLines(filepath.Join("data", "logs", "nodectl.log"), limit)
	if err != nil {
		return nil, err
	}

	entries := make([]RecentLogEntry, 0, len(lines))
	for i := len(lines) - 1; i >= 0; i-- {
		entries = append(entries, parseRecentLogLine(lines[i]))
	}

	return entries, nil
}

func readRecentLogLines(logPath string, limit int) ([]string, error) {
	f, err := os.Open(logPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lines := make([]string, 0, limit)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}

	return lines, nil
}

func parseRecentLogLine(line string) RecentLogEntry {
	msg := extractLogField(logMsgReg, line)
	msg = decodeLogValue(msg)

	level := strings.ToUpper(extractLogField(logLevelReg, line))
	if level == "" {
		level = "INFO"
	}

	entry := RecentLogEntry{
		Time:      extractLogField(logTimeReg, line),
		Level:     level,
		LevelCN:   levelToCN(level),
		Source:    extractLogField(logSourceReg, line),
		Message:   msg,
		MessageCN: translateLogMessage(msg),
		Operation: summarizeLogOperation(msg),
		Raw:       line,
	}

	if entry.MessageCN == "" {
		entry.MessageCN = msg
	}

	return entry
}

func extractLogField(reg *regexp.Regexp, line string) string {
	matches := reg.FindStringSubmatch(line)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}

func decodeLogValue(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if strings.HasPrefix(v, "\"") && strings.HasSuffix(v, "\"") {
		if unquoted, err := strconv.Unquote(v); err == nil {
			return unquoted
		}
	}
	return v
}

func levelToCN(level string) string {
	switch strings.ToUpper(level) {
	case "DEBUG":
		return "调试"
	case "WARN", "WARNING":
		return "警告"
	case "ERROR":
		return "错误"
	default:
		return "信息"
	}
}

func summarizeLogOperation(msg string) string {
	if msg == "" {
		return "系统日志"
	}

	switch {
	case strings.Contains(msg, "登录"):
		return "用户登录相关"
	case strings.Contains(msg, "退出"):
		return "用户退出相关"
	case strings.Contains(msg, "节点"):
		return "节点管理操作"
	case strings.Contains(msg, "证书") || strings.Contains(msg, "ACME"):
		return "证书管理操作"
	case strings.Contains(msg, "GeoIP"):
		return "GeoIP 数据操作"
	case strings.Contains(msg, "Mihomo"):
		return "Mihomo 核心操作"
	case strings.Contains(msg, "重启"):
		return "系统重启操作"
	case strings.Contains(msg, "配置") || strings.Contains(msg, "设置"):
		return "配置变更操作"
	case strings.Contains(msg, "订阅"):
		return "订阅相关操作"
	default:
		return "系统日志"
	}
}

func translateLogMessage(msg string) string {
	if msg == "" {
		return ""
	}

	replacer := strings.NewReplacer(
		"service started", "服务已启动",
		"server started", "服务已启动",
		"restarting", "正在重启",
		"restart", "重启",
		"updated", "已更新",
		"update", "更新",
		"failed", "失败",
		"success", "成功",
		"warning", "警告",
		"error", "错误",
		"not found", "未找到",
		"forbidden", "禁止访问",
		"timeout", "超时",
	)

	translated := replacer.Replace(msg)

	if translated != msg {
		return translated
	}

	lower := strings.ToLower(msg)
	if strings.Contains(lower, "fail") {
		return "操作失败: " + msg
	}
	if strings.Contains(lower, "success") {
		return "操作成功: " + msg
	}
	if strings.Contains(lower, "start") {
		return "系统启动相关: " + msg
	}
	if strings.Contains(lower, "login") {
		return "登录相关: " + msg
	}

	return msg
}
