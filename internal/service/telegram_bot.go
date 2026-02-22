package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"nodectl/internal/database"
	"nodectl/internal/logger"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

var (
	tgBotCancel context.CancelFunc
)

// StartTelegramBot 初始化并启动 Telegram Bot 监听协程
func StartTelegramBot() {
	// 等待数据库初始化完成
	time.Sleep(2 * time.Second)
	RestartTelegramBot()
}

// RestartTelegramBot 重启 Telegram Bot 服务，用于热更新配置
func RestartTelegramBot() {
	if tgBotCancel != nil {
		tgBotCancel()
		tgBotCancel = nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	tgBotCancel = cancel

	go runTelegramBot(ctx)
}

func runTelegramBot(ctx context.Context) {
	var enabledConfig database.SysConfig
	var tokenConfig database.SysConfig
	var whitelistConfig database.SysConfig
	var registerConfig database.SysConfig

	// 获取配置
	database.DB.Where("key = ?", "tg_bot_enabled").First(&enabledConfig)
	database.DB.Where("key = ?", "tg_bot_token").First(&tokenConfig)
	database.DB.Where("key = ?", "tg_bot_whitelist").First(&whitelistConfig)
	database.DB.Where("key = ?", "tg_bot_register_commands").First(&registerConfig)

	botEnabled := strings.TrimSpace(enabledConfig.Value) == "true"
	token := strings.TrimSpace(tokenConfig.Value)
	whitelist := strings.TrimSpace(whitelistConfig.Value)
	registerCommands := strings.TrimSpace(registerConfig.Value) == "true"

	if !botEnabled {
		return
	}

	if token == "" || whitelist == "" {
		return
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		logger.Log.Error("Telegram Bot 初始化失败", "error", err)
		return
	}

	// bot.Debug = true // 可选：开启调试模式

	if registerCommands {
		// 删除并重新注册主菜单指令
		deleteConfig := tgbotapi.NewDeleteMyCommands()
		if _, err := bot.Request(deleteConfig); err != nil {
			logger.Log.Warn("清除历史 TG Bot 指令失败", "error", err)
		}

		setCmdConfig := tgbotapi.NewSetMyCommands(
			tgbotapi.BotCommand{
				Command:     "sub",
				Description: "获取我的节点订阅或机场资源",
			},
		)
		if _, err := bot.Request(setCmdConfig); err != nil {
			logger.Log.Error("注册 TG Bot 指令失败", "error", err)
		}
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			logger.Log.Info("检测到配置变化，正在停止旧的 Telegram Bot 服务...")
			bot.StopReceivingUpdates()
			return
		case update, ok := <-updates:
			if !ok {
				return
			}
			// 处理消息 (Message)
			if update.Message != nil {
				handleMessage(bot, update.Message, whitelist)
			} else if update.CallbackQuery != nil {
				handleCallbackQuery(bot, update.CallbackQuery, whitelist)
			}
		}
	}
}

// 检查用户是否在白名单中
func isUserAllowed(userID int64, whitelistStr string) bool {
	if whitelistStr == "" {
		return false // 未配置白名单时，默认拒绝所有人
	}
	whitelist := strings.Split(whitelistStr, ",")
	userIDStr := fmt.Sprintf("%d", userID)
	for _, item := range whitelist {
		parts := strings.SplitN(item, "=", 2)
		id := strings.TrimSpace(parts[0])
		if id == userIDStr {
			return true
		}
	}
	return false
}

// 获取基础面板域名和鉴权 Token
func getBaseURLAndToken() (string, string) {
	var panelURLConfig database.SysConfig
	var subTokenConfig database.SysConfig

	database.DB.Where("key = ?", "panel_url").First(&panelURLConfig)
	database.DB.Where("key = ?", "sub_token").First(&subTokenConfig)

	panelURL := strings.TrimRight(strings.TrimSpace(panelURLConfig.Value), "/")
	if panelURL == "" {
		panelURL = "https://yourdomain.com" // 默认占位
	}
	return panelURL, strings.TrimSpace(subTokenConfig.Value)
}

// 处理普通消息
func handleMessage(bot *tgbotapi.BotAPI, message *tgbotapi.Message, whitelistStr string) {
	if !message.IsCommand() {
		return // 目前只响应命令
	}

	// 白名单验证
	if !isUserAllowed(message.From.ID, whitelistStr) {
		logger.Log.Warn("未授权的 TG 用户尝试访问", "user_id", message.From.ID)
		msg := tgbotapi.NewMessage(message.Chat.ID, "⛔ 未经授权的访问。请将您的 TG ID 添加到管理员白名单中。")
		bot.Send(msg)
		return
	}

	switch message.Command() {
	case "start", "sub":
		sendSubMenu(bot, message.Chat.ID)
	default:
		msg := tgbotapi.NewMessage(message.Chat.ID, "未知命令。请发送 /sub 获取订阅菜单。")
		bot.Send(msg)
	}
}

// 发送订阅主菜单 (包含一级按钮)
func sendSubMenu(bot *tgbotapi.BotAPI, chatID int64) {
	msg := tgbotapi.NewMessage(chatID, "🚀 请选择菜单")

	// 创建 Inline Keyboard，包含一级菜单按钮
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🌐 订阅中心", "menu_sub_center"),
			tgbotapi.NewInlineKeyboardButtonData("📡 服务器列表", "menu_nodes:1"),
		),
	)

	msg.ReplyMarkup = keyboard
	bot.Send(msg)
}

// 处理点击 Inline Keyboard 回调事件
func handleCallbackQuery(bot *tgbotapi.BotAPI, callbackQuery *tgbotapi.CallbackQuery, whitelistStr string) {
	// 同样需要进行白名单验证 (防止之前的消息被转发或未授权用户点击)
	if !isUserAllowed(callbackQuery.From.ID, whitelistStr) {
		bot.Request(tgbotapi.NewCallback(callbackQuery.ID, "⛔ 未经授权"))
		return
	}

	// 响应 Telegram 提示收到回调，消除按钮顶部的 loading 状态
	bot.Request(tgbotapi.NewCallback(callbackQuery.ID, ""))

	data := callbackQuery.Data
	chatID := callbackQuery.Message.Chat.ID
	messageID := callbackQuery.Message.MessageID

	switch data {
	case "menu_sub_center":
		// 点击“订阅中心”，修改原消息，显示二级菜单
		editMsg := tgbotapi.NewEditMessageText(chatID, messageID, "👇 请选择具体的订阅格式：")
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🐱 Clash 订阅", "get_sub_clash"),
				tgbotapi.NewInlineKeyboardButtonData("✌️ V2ray 订阅", "get_sub_v2ray"),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🔄 重置订阅", "reset_sub_token"),
				tgbotapi.NewInlineKeyboardButtonData("🔙 返回上级", "menu_main"),
			),
		)
		editMsg.ReplyMarkup = &keyboard
		bot.Send(editMsg)

	case "menu_main":
		// 返回主菜单
		editMsg := tgbotapi.NewEditMessageText(chatID, messageID, "🚀 请选择要获取的订阅类型：")
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🌐 订阅中心", "menu_sub_center"),
				tgbotapi.NewInlineKeyboardButtonData("📡 服务器列表", "menu_nodes:1"),
			),
		)
		editMsg.ReplyMarkup = &keyboard
		bot.Send(editMsg)

	case "get_sub_clash":
		panelURL, token := getBaseURLAndToken()
		subURL := fmt.Sprintf("%s/sub/clash?token=%s", panelURL, token)
		replyText := fmt.Sprintf("✅ **您的 Clash (Mihomo) 订阅链接：**\n\n`%s`", subURL)

		msg := tgbotapi.NewMessage(chatID, replyText)
		msg.ParseMode = "Markdown"
		sentMsg, err := bot.Send(msg)
		if err == nil {
			go func(chatID int64, messageID int) {
				time.Sleep(10 * time.Second)
				deleteMsg := tgbotapi.NewDeleteMessage(chatID, messageID)
				bot.Request(deleteMsg)
			}(chatID, sentMsg.MessageID)
		}

	case "get_sub_v2ray":
		panelURL, token := getBaseURLAndToken()
		subURL := fmt.Sprintf("%s/sub/v2ray?token=%s", panelURL, token)
		replyText := fmt.Sprintf("✅ **您的 V2ray 订阅链接：**\n\n`%s`", subURL)

		msg := tgbotapi.NewMessage(chatID, replyText)
		msg.ParseMode = "Markdown"
		sentMsg, err := bot.Send(msg)
		if err == nil {
			go func(chatID int64, messageID int) {
				time.Sleep(10 * time.Second)
				deleteMsg := tgbotapi.NewDeleteMessage(chatID, messageID)
				bot.Request(deleteMsg)
			}(chatID, sentMsg.MessageID)
		}

	case "reset_sub_token":
		secureBytes := make([]byte, 16)
		rand.Read(secureBytes)
		newToken := hex.EncodeToString(secureBytes)

		err := database.DB.Model(&database.SysConfig{}).Where("key = ?", "sub_token").Update("value", newToken).Error
		if err != nil {
			logger.Log.Error("TG Bot 重置 Sub Token 失败", "error", err)
			msg := tgbotapi.NewMessage(chatID, "❌ 重置订阅 Token 失败，请检查系统日志。")
			bot.Send(msg)
			return
		}

		logger.Log.Info("用户通过 TG Bot 重置了订阅 Token", "user_id", callbackQuery.From.ID)
		replyText := "✅ **订阅重置成功！**"

		msg := tgbotapi.NewMessage(chatID, replyText)
		msg.ParseMode = "Markdown"
		bot.Send(msg)

	default:
		if strings.HasPrefix(data, "menu_nodes:") {
			handleMenuNodes(bot, chatID, messageID, data)
		} else if strings.HasPrefix(data, "node_info:") {
			handleNodeInfo(bot, chatID, messageID, data)
		}
	}
}

func formatBytes(b int64) string {
	if b == 0 {
		return "0G"
	}
	gb := float64(b) / (1024 * 1024 * 1024)
	if gb >= 1024 {
		return fmt.Sprintf("%.2fT", gb/1024)
	}
	return fmt.Sprintf("%.2fG", gb)
}

func handleMenuNodes(bot *tgbotapi.BotAPI, chatID int64, messageID int, data string) {
	pageStr := strings.TrimPrefix(data, "menu_nodes:")
	page, err := strconv.Atoi(pageStr)
	if err != nil || page < 1 {
		page = 1
	}

	var totalNodes int64
	database.DB.Model(&database.NodePool{}).Where("is_blocked = ?", false).Count(&totalNodes)

	if totalNodes == 0 {
		editMsg := tgbotapi.NewEditMessageText(chatID, messageID, "📭 当前没有任何节点。")
		btn := tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🔙 返回上级", "menu_main"))
		keyboard := tgbotapi.NewInlineKeyboardMarkup(btn)
		editMsg.ReplyMarkup = &keyboard
		bot.Send(editMsg)
		return
	}

	perPage := 12
	totalPages := int(math.Ceil(float64(totalNodes) / float64(perPage)))
	if page > totalPages {
		page = totalPages
	}

	offset := (page - 1) * perPage

	var nodes []database.NodePool
	// 按照流量消耗排行，一页显示12个
	database.DB.Where("is_blocked = ?", false).
		Order("(traffic_up + traffic_down) DESC").
		Limit(perPage).Offset(offset).Find(&nodes)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 **服务器流量排行榜 (第 %d/%d 页)**\n\n", page, totalPages))

	getStringWidth := func(s string) int {
		width := 0
		for _, r := range s {
			if r > 127 {
				width += 2
			} else {
				width += 1
			}
		}
		return width
	}

	var maxNameWidth int
	var maxUsedWidth int

	type listNodeInfo struct {
		id    string
		name  string
		used  string
		limit string
	}
	var infos []listNodeInfo

	for _, node := range nodes {
		nameWidth := getStringWidth(node.Name)
		if nameWidth > maxNameWidth {
			maxNameWidth = nameWidth
		}

		usedStr := formatBytes(node.TrafficUp + node.TrafficDown)
		usedWidth := getStringWidth(usedStr)
		if usedWidth > maxUsedWidth {
			maxUsedWidth = usedWidth
		}

		limitStr := "不限量"
		if node.TrafficLimit > 0 {
			limitStr = formatBytes(node.TrafficLimit)
		}
		infos = append(infos, listNodeInfo{node.UUID, node.Name, usedStr, limitStr})
	}

	for _, info := range infos {
		namePad := maxNameWidth - getStringWidth(info.name)
		if namePad < 0 {
			namePad = 0
		}
		paddedName := info.name + strings.Repeat(" ", namePad)

		usedPad := maxUsedWidth - getStringWidth(info.used)
		if usedPad < 0 {
			usedPad = 0
		}
		paddedUsed := strings.Repeat(" ", usedPad) + info.used

		sb.WriteString(fmt.Sprintf("`%s | %s | %s`\n", paddedName, paddedUsed, info.limit))
	}

	editMsg := tgbotapi.NewEditMessageText(chatID, messageID, sb.String())
	editMsg.ParseMode = "Markdown"

	// 构建键盘
	var keyboard [][]tgbotapi.InlineKeyboardButton

	var currentRow []tgbotapi.InlineKeyboardButton
	for i, n := range nodes {
		currentRow = append(currentRow, tgbotapi.NewInlineKeyboardButtonData(n.Name, "node_info:"+n.UUID))
		if len(currentRow) == 3 || i == len(nodes)-1 {
			keyboard = append(keyboard, currentRow)
			currentRow = nil
		}
	}

	// 翻页行
	var pageRow []tgbotapi.InlineKeyboardButton
	if page > 1 {
		pageRow = append(pageRow, tgbotapi.NewInlineKeyboardButtonData("◀️ 上一页", fmt.Sprintf("menu_nodes:%d", page-1)))
	}
	if page < totalPages {
		pageRow = append(pageRow, tgbotapi.NewInlineKeyboardButtonData("▶️ 下一页", fmt.Sprintf("menu_nodes:%d", page+1)))
	}
	if len(pageRow) > 0 {
		keyboard = append(keyboard, pageRow)
	}

	// 返回行
	keyboard = append(keyboard, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("🔙 返回主菜单", "menu_main"),
	))

	kbd := tgbotapi.NewInlineKeyboardMarkup(keyboard...)
	editMsg.ReplyMarkup = &kbd
	bot.Send(editMsg)
}

func handleNodeInfo(bot *tgbotapi.BotAPI, chatID int64, messageID int, data string) {
	uuid := strings.TrimPrefix(data, "node_info:")
	var node database.NodePool
	if err := database.DB.Where("uuid = ?", uuid).First(&node).Error; err != nil {
		bot.Send(tgbotapi.NewEditMessageText(chatID, messageID, "❌ 找不到该节点信息。"))
		return
	}

	routingType := "直连节点"
	if node.RoutingType != 1 {
		routingType = "中转/落地节点"
	}

	upStr := formatBytes(node.TrafficUp)
	downStr := formatBytes(node.TrafficDown)
	usedStr := formatBytes(node.TrafficUp + node.TrafficDown)
	limitStr := "不限量"
	if node.TrafficLimit > 0 {
		limitStr = formatBytes(node.TrafficLimit)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("💻 **[%s] 的专属信息**\n\n", node.Name))
	sb.WriteString(fmt.Sprintf("▪️ **网络组：** %s\n", routingType))
	sb.WriteString(fmt.Sprintf("▪️ **已用上传：** %s\n", upStr))
	sb.WriteString(fmt.Sprintf("▪️ **已用下载：** %s\n", downStr))
	sb.WriteString(fmt.Sprintf("▪️ **总计消耗：** %s / %s\n", usedStr, limitStr))
	if node.Remark != "" {
		sb.WriteString(fmt.Sprintf("▪️ **备注：** %s\n", node.Remark))
	}

	sb.WriteString("\n🔗 **协议链接：**\n")
	hasLink := false
	for protoType, link := range node.Links {
		if link != "" {
			sb.WriteString(fmt.Sprintf("**%s**: `%s`\n", strings.ToUpper(protoType), link))
			hasLink = true
		}
	}

	if !hasLink {
		sb.WriteString("⚠️ 该节点目前没有任何协议链接。\n")
	}

	editMsg := tgbotapi.NewEditMessageText(chatID, messageID, sb.String())
	editMsg.ParseMode = "Markdown"

	keyboard := [][]tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔙 返回服务器列表", "menu_nodes:1"),
		),
	}

	kbd := tgbotapi.NewInlineKeyboardMarkup(keyboard...)
	editMsg.ReplyMarkup = &kbd
	bot.Send(editMsg)
}
