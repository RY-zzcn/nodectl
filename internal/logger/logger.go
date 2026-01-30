package logger

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	logFilePath   = "./logs/nodectl.log" // 日志文件路径
	logMaxSize    = 2                    // 单个文件最大尺寸 (MB)
	logMaxBackups = 5                    // 保留旧文件最大个数
	logMaxAge     = 7                    // 保留旧文件最大天数
	logCompress   = true                 // 是否压缩旧日志
	consoleOutput = true                 // 是否同时输出到控制台

	// 当前开发阶段建议使用 Debug，上线时改为 Info
	currentLevel = slog.LevelDebug
)

func Setup() {

	logDir := filepath.Dir(logFilePath)
	// 确保日志目录存在
	if _, err := os.Stat(logDir); os.IsNotExist(err) {
		_ = os.MkdirAll(logDir, 0755)
	}
	// 记录到log文件，也可以选择同时输出到控制台
	fileWriter := &lumberjack.Logger{
		Filename:   logFilePath,
		MaxSize:    logMaxSize,
		MaxBackups: logMaxBackups,
		MaxAge:     logMaxAge,
		Compress:   logCompress,
	}

	var writers []io.Writer
	writers = append(writers, fileWriter)

	if consoleOutput {
		writers = append(writers, os.Stdout)
	}
	multiWriter := io.MultiWriter(writers...)
	handler := slog.NewJSONHandler(multiWriter, &slog.HandlerOptions{
		Level:     currentLevel,
		AddSource: true, // 极其重要：日志会显示是哪个文件第几行打印的
	})
	// 设置为全局默认的 logger
	slog.SetDefault(slog.New(handler))
}
