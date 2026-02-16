package logger

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings" // [新增] 引入 strings 包

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

	// 2. 配置 slog 拦截器 (修复 Docker 下路径过长的问题)
	opts := &slog.HandlerOptions{
		Level:     slog.LevelDebug,
		AddSource: true,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// 格式化时间：去掉毫秒和时区，只保留到秒
			if a.Key == slog.TimeKey {
				return slog.String(slog.TimeKey, a.Value.Time().Format("2006-01-02 15:04:05"))
			}

			// [核心修复] 智能格式化源码路径，剥离宿主机的绝对路径干扰
			if a.Key == slog.SourceKey {
				source, ok := a.Value.Any().(*slog.Source)
				if ok {
					file := filepath.ToSlash(source.File)

					// 智能截取路径：寻找项目的特征目录
					if idx := strings.Index(file, "/internal/"); idx != -1 {
						file = file[idx+1:] // 只保留 "internal/..." 开始的路径
					} else if strings.HasSuffix(file, "main.go") {
						file = "main.go" // 根目录的 main.go 直接显示
					} else {
						// 兜底方案：只保留路径的最后两级
						parts := strings.Split(file, "/")
						if len(parts) > 2 {
							file = strings.Join(parts[len(parts)-2:], "/")
						}
					}

					formattedSource := fmt.Sprintf("%s:%d", file, source.Line)
					return slog.String(slog.SourceKey, formattedSource)
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
