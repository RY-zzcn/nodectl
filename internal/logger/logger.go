package logger

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Log 全局导出的日志实例
var Log *slog.Logger

// Init 初始化日志配置
func Init() {
	// 1. 配置文件切割
	logFile := &lumberjack.Logger{
		Filename:   filepath.Join("data", "logs", "nodectl.log"),
		MaxSize:    10,
		MaxBackups: 5,
		MaxAge:     30,
		Compress:   true,
	}

	multiWriter := io.MultiWriter(os.Stdout, logFile)

	// 获取当前工作目录，用于计算相对路径
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "" // 降级处理
	}

	// 2. 配置 slog 拦截器 (核心魔法在这里)
	opts := &slog.HandlerOptions{
		Level:     slog.LevelDebug,
		AddSource: true,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// 格式化时间：去掉毫秒和时区，只保留到秒
			if a.Key == slog.TimeKey {
				return slog.String(slog.TimeKey, a.Value.Time().Format("2006-01-02 15:04:05"))
			}

			// 格式化路径：从绝对路径改为相对路径，并将 Windows 的反斜杠转换为斜杠
			if a.Key == slog.SourceKey {
				source, ok := a.Value.Any().(*slog.Source)
				if ok && cwd != "" {
					relPath, err := filepath.Rel(cwd, source.File)
					if err == nil {
						// 拼接成 "目录/文件.go:行号" 的精简格式
						formattedSource := fmt.Sprintf("%s:%d", filepath.ToSlash(relPath), source.Line)
						return slog.String(slog.SourceKey, formattedSource)
					}
				}
			}

			return a
		},
	}

	// 3. 实例化 Logger
	handler := slog.NewTextHandler(multiWriter, opts)
	Log = slog.New(handler)
	slog.SetDefault(Log)

	Log.Info("日志组件初始化完成", slog.String("模块", "logger"))
}
