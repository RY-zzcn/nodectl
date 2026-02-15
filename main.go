package main

import (
	"embed"
	"nodectl/internal/database"
	"nodectl/internal/logger"
	"nodectl/internal/server"
)

//go:embed templates/*
var templatesFS embed.FS

func main() {
	// 1. 初始化日志组件
	logger.Init()

	// 2.初始化数据库
	database.InitDB()

	// 3. 启动 Web 服务 (将打包好的前端模板传入)
	server.Start(templatesFS)

}
