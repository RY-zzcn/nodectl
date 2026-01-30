package model

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// 数据库文件路径
const DBPath = "./data/nodectl.db"

func Init() *gorm.DB {
	db, err := connectDB()
	if err != nil {
		slog.Error("致命错误：无法连接数据库", "err", err)
		os.Exit(1)
	}

	if err := migrateTables(db); err != nil {
		slog.Error("致命错误：数据库表结构初始化失败", "err", err)
		os.Exit(1)
	}

	slog.Info("数据库及表结构初始化全部完成")
	return db
}

func connectDB() (*gorm.DB, error) {
	// 1. 确保数据目录存在
	dir := filepath.Dir(DBPath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("创建数据库目录失败: %w", err)
		}
	}

	// 2. 连接 SQLite 数据库
	db, err := gorm.Open(sqlite.Open(DBPath), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("SQLite 连接失败: %w", err)
	}

	// 3. 开启外键约束
	if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		slog.Warn("无法开启 SQLite 外键支持，级联删除可能失效", "err", err)
	}

	slog.Debug("数据库连接建立成功", "path", DBPath)
	return db, nil
}

// migrateTables 初始化并自动迁移所有数据库表结构 (改为私有)
func migrateTables(db *gorm.DB) error {
	slog.Info("开始初始化数据库表结构")

	// 1. 静态表迁移
	if err := db.AutoMigrate(
		&NodePool{},     // 节点池表
		&SystemConfig{}, // 系统配置表
	); err != nil {
		return fmt.Errorf("静态表迁移失败: %w", err)
	}
	slog.Debug("静态表结构校验完成")

	// 2. 动态表迁移 (sub_x)
	var existingTables []string
	if err := db.Raw("SELECT name FROM sqlite_master WHERE type='table'").Scan(&existingTables).Error; err != nil {
		return fmt.Errorf("获取表列表失败: %w", err)
	}

	hasDefaultSub := false
	migratedCount := 0

	for _, tableName := range existingTables {
		if strings.HasPrefix(tableName, "sub_") {
			slog.Debug("正在检查并更新动态表结构", "table", tableName)

			if err := db.Table(tableName).AutoMigrate(&Subscription{}); err != nil {
				return fmt.Errorf("动态表 %s 迁移失败: %w", tableName, err)
			}

			migratedCount++
			if tableName == "sub_0" {
				hasDefaultSub = true
			}
		}
	}

	if migratedCount > 0 {
		slog.Debug("动态订阅表迁移完成", "count", migratedCount)
	}

	// 3. 确保至少存在 sub_0 表
	if !hasDefaultSub {
		slog.Info("检测到缺少默认订阅表，正在创建 sub_0")
		if err := db.Table("sub_0").AutoMigrate(&Subscription{}); err != nil {
			return fmt.Errorf("创建默认订阅表 sub_0 失败: %w", err)
		}
	}

	return nil
}
