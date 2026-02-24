package main

import (
	"embed"
	"nodectl/internal/database"
	"nodectl/internal/logger"
	"nodectl/internal/server"
	"nodectl/internal/version"
	"os"
	"time"
)

//go:embed templates/*
var templatesFS embed.FS

func main() {
	// 0. 强制使用北京时间，避免容器/宿主机时区差异影响统计
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		time.Local = loc
		_ = os.Setenv("TZ", "Asia/Shanghai")
	}

	// 1. 初始化日志组件
	logger.Init(logger.LoadPersistedLogLevel())
	logger.Log.Debug("Nodectl Core 正在启动", "版本号", version.Version)
	// 2.初始化数据库
	database.InitDB()

	// 3. 启动 Web 服务 (将打包好的前端模板传入)
	server.Start(templatesFS)

}
