package main

import (
	"log/slog"
	"nodectl/internal/logger" // 引入日志包
	"nodectl/internal/model"
)

func main() {
	// 1. 初始化日志系统
	logger.Setup()
	slog.Info("Nodectl 服务正在启动...")
	// 2. 数据库连接
	db := model.Init()
	slog.Info("系统就绪，数据库加载正常")
	_ = db
}
