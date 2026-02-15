package database

import (
	"crypto/rand"
	"encoding/hex"
	"errors"

	"nodectl/internal/logger"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// initDefaultConfigs 初始化默认的系统配置参数
func initDefaultConfigs() {
	// 1. 初始化普通基础设置
	initBasicSettings()

	// 2. 初始化核心安全设置 (加密密钥、默认账号密码)
	initAuthSettings()
}

func initBasicSettings() {
	defaultConfigs := []SysConfig{
		{Key: "panel_title", Value: "Nodectl 核心控制台", Description: "网站面板标题"},
	}

	for _, config := range defaultConfigs {
		err := DB.Where(SysConfig{Key: config.Key}).FirstOrCreate(&config).Error
		if err != nil {
			logger.Log.Error("初始化普通配置失败", "key", config.Key, "err", err.Error())
		}
	}
}

func initAuthSettings() {
	// 1. 初始化随机加密密钥 (JWT Secret / Session Key)
	var secretConfig SysConfig
	err := DB.Where("key = ?", "jwt_secret").First(&secretConfig).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// 只有当密钥不存在时才生成
		secureBytes := make([]byte, 32)
		if _, err := rand.Read(secureBytes); err != nil {
			panic("无法生成安全随机密钥: " + err.Error())
		}
		randomSecret := hex.EncodeToString(secureBytes)

		DB.Create(&SysConfig{
			Key:         "jwt_secret",
			Value:       randomSecret,
			Description: "系统核心加密密钥 (请勿泄露)",
		})
		logger.Log.Info("已生成全新的系统加密密钥")
	}

	// 2. 初始化默认管理员账号和密码
	var adminUser SysConfig
	err = DB.Where("key = ?", "admin_username").First(&adminUser).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// 生成 bcrypt 哈希密码 (默认密码设为 admin)
		defaultPassword := "admin"
		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(defaultPassword), bcrypt.DefaultCost)
		if err != nil {
			panic("默认密码加密失败: " + err.Error())
		}

		// 存入用户名
		DB.Create(&SysConfig{Key: "admin_username", Value: "admin", Description: "管理员登录账号"})
		// 存入密码哈希值
		DB.Create(&SysConfig{Key: "admin_password", Value: string(hashedPassword), Description: "管理员密码(Bcrypt加密)"})

		logger.Log.Warn("已创建默认管理员账号！", "用户名", "admin", "密码", "admin", "提示", "请登录后尽快修改！")
	}
}
