package service

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"nodectl/internal/database"
	"nodectl/internal/logger"

	"gopkg.in/yaml.v3"
)

// SyncAirportSubscription 执行订阅更新核心逻辑
func SyncAirportSubscription(subID string) error {
	var sub database.AirportSub
	if err := database.DB.First(&sub, "id = ?", subID).Error; err != nil {
		return err
	}

	// 1. 下载订阅内容
	resp, err := http.Get(sub.URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	content := string(bodyBytes)

	// 2. 解析节点链接 (自动识别 Base64 或 Clash)
	var newLinks []NodeItem
	if isClashYAML(content) {
		newLinks = parseClashYAML(bodyBytes)
	} else {
		newLinks = parseBase64Sub(content)
	}

	if len(newLinks) == 0 {
		logger.Log.Warn("订阅未解析到任何节点", "sub_name", sub.Name)
		return nil
	}

	// 3. 获取旧节点状态 (Name -> RoutingType 映射)
	var oldNodes []database.AirportNode
	database.DB.Where("sub_id = ?", subID).Find(&oldNodes)
	statusMap := make(map[string]int)
	for _, node := range oldNodes {
		if node.RoutingType != 0 {
			statusMap[node.Name] = node.RoutingType
		}
	}

	// 4. 事务更新：删除旧节点 -> 插入新节点 (保留状态)

	var filterConf database.SysConfig
	filterInvalid := false
	if err := database.DB.Where("key = ?", "airport_filter_invalid").First(&filterConf).Error; err == nil {
		filterInvalid = (filterConf.Value == "true")
	}

	tx := database.DB.Begin()

	if err := tx.Where("sub_id = ?", subID).Delete(&database.AirportNode{}).Error; err != nil {
		tx.Rollback()
		return err
	}

	var nodesToInsert []database.AirportNode
	for i, item := range newLinks {
		// 传入完整的特征 (Name, Server, Port) 进行立体鉴定
		if filterInvalid && isInvalidNode(item.Name, item.Server, item.Port) {
			logger.Log.Debug("剔除无效机场节点", "name", item.Name, "server", item.Server, "port", item.Port)
			continue
		}

		routingType := 0
		if oldStatus, ok := statusMap[item.Name]; ok {
			routingType = oldStatus
		}

		nodesToInsert = append(nodesToInsert, database.AirportNode{
			SubID:         subID,
			Name:          item.Name,
			Protocol:      item.Protocol,
			Link:          item.Link,
			OriginalIndex: i,
			RoutingType:   routingType,
		})
	}

	if err := tx.Create(&nodesToInsert).Error; err != nil {
		tx.Rollback()
		return err
	}

	tx.Model(&sub).Update("updated_at", time.Now())
	tx.Commit()
	return nil
}

// NodeItem 临时解析结构
type NodeItem struct {
	Name     string
	Protocol string
	Link     string
	Server   string
	Port     int
}

// ------------------- [无效节点过滤引擎] -------------------
// isInvalidNode 判断节点是否为虚假广告/无效信息节点 (名称+结构多维鉴定)
func isInvalidNode(name string, server string, port int) bool {
	// 1. [绝对精准] 结构级防伪：端口非法
	if port <= 0 || port > 65535 {
		return true
	}

	// 2. [绝对精准] 结构级防伪：拦截常见的虚假占位 IP
	dummyServers := map[string]bool{
		"127.0.0.1": true, "0.0.0.0": true, "localhost": true,
		"1.1.1.1": true, "8.8.8.8": true, "1.0.0.1": true,
		"114.114.114.114": true,
	}
	if dummyServers[server] {
		return true
	}

	// 3. [正则防伪] 匹配日期范围特征，拦截如: "2.17 - 2.19"
	datePattern := regexp.MustCompile(`\d{1,4}[-./]\d{1,2}\s*[-~至]\s*\d{1,4}[-./]\d{1,2}`)
	if datePattern.MatchString(name) {
		return true
	}

	// 4. [正则防伪] 拦截名称中包含域名或 IP 地址的节点
	ipPattern := regexp.MustCompile(`\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`)
	if ipPattern.MatchString(name) {
		return true
	}

	domainPattern := regexp.MustCompile(`(?i)[a-zA-Z0-9-]+\.(com|cn|net|org|xyz|io|me|top|pw|cc|info|biz|tv|ws|co|uk)`)
	if domainPattern.MatchString(name) {
		return true
	}

	// 5. [常规防伪]
	keywords := []string{
		"到期", "剩余", "流量", "过期", "官网", "套餐", "重置",
		"更新", "群", "广告", "折", "优惠", "福利", "公告",
		"时间", "地址", "频道", "失联", "防联",
	}
	for _, kw := range keywords {
		if strings.Contains(name, kw) {
			return true
		}
	}
	return false
}

// ------------------- [名称清洗引擎] -------------------

// cleanNodeName 清理节点名称，去除首尾和开头的无意义符号
func cleanNodeName(name string) string {
	name = strings.TrimSpace(name)
	// 去除开头的无意义符号（横杠、下划线、竖线、星号等）以及它们附带的空格(含全角)
	name = strings.TrimLeft(name, "-_|=+* \t　")
	name = strings.TrimSpace(name)
	if name == "" {
		return "未命名节点"
	}
	return name
}

// ------------------- [解析与转换逻辑] -------------------

func isClashYAML(content string) bool {
	return strings.Contains(content, "proxies:")
}

// parseBase64Sub 解析传统 Base64 订阅
func parseBase64Sub(content string) []NodeItem {
	decoded := safeBase64Decode(content) // 复用 links.go 中的 safeBase64Decode
	lines := strings.Split(strings.ReplaceAll(decoded, "\r\n", "\n"), "\n")
	var result []NodeItem

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		node := ParseLinkToClashNode(line, "")
		if node != nil {
			cleanName := cleanNodeName(node.Name)
			result = append(result, NodeItem{
				Name:     cleanName,
				Protocol: node.Type,
				Link:     line,
				Server:   node.Server, // 提取服务器地址供鉴定
				Port:     node.Port,   // 提取端口供鉴定
			})
		}
	}
	return result
}

// parseClashYAML 解析 Clash YAML 并转换回分享链接
func parseClashYAML(content []byte) []NodeItem {
	var raw struct {
		Proxies []map[string]interface{} `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(content, &raw); err != nil {
		logger.Log.Error("解析 Clash 订阅 YAML 失败", "error", err)
		return nil
	}

	var result []NodeItem
	for _, p := range raw.Proxies {
		name := getString(p["name"])
		server := getString(p["server"])
		if name == "" || server == "" {
			continue
		}

		cleanName := cleanNodeName(name)
		link := convertClashProxyToLink(p, cleanName)

		if link != "" {
			result = append(result, NodeItem{
				Name:     cleanName,
				Protocol: getString(p["type"]),
				Link:     link,
				Server:   server,            // 已在上方提取，直接传入
				Port:     getInt(p["port"]), // 直接提取端口
			})
		}
	}
	return result
}

// convertClashProxyToLink 核心转换器：Clash Map -> 标准 URI
func convertClashProxyToLink(p map[string]interface{}, cleanName string) string {
	protoType := getString(p["type"])
	server := getString(p["server"])
	port := getInt(p["port"])
	if port == 0 {
		return ""
	}

	switch protoType {
	case "ss":
		cipher := getString(p["cipher"])
		password := getString(p["password"])
		auth := base64.StdEncoding.EncodeToString([]byte(cipher + ":" + password))
		return fmt.Sprintf("ss://%s@%s:%d#%s", auth, server, port, url.PathEscape(cleanName))

	case "trojan":
		password := getString(p["password"])
		sni := getString(p["sni"])
		network := getString(p["network"])

		link := fmt.Sprintf("trojan://%s@%s:%d?sni=%s", password, server, port, sni)
		if network != "" {
			link += "&type=" + network
			if wsOpts, ok := p["ws-opts"].(map[string]interface{}); ok {
				if path := getString(wsOpts["path"]); path != "" {
					link += "&path=" + url.QueryEscape(path)
				}
				if headers, ok := wsOpts["headers"].(map[string]interface{}); ok {
					if host := getString(headers["Host"]); host != "" {
						link += "&host=" + url.QueryEscape(host)
					}
				}
			}
		}
		return link + "#" + url.PathEscape(cleanName)

	case "vmess":
		uuid := getString(p["uuid"])
		cipher := getString(p["cipher"])
		if cipher == "" {
			cipher = "auto"
		}
		network := getString(p["network"])
		if network == "" {
			network = "tcp"
		}

		vj := map[string]interface{}{
			"v":    "2",
			"ps":   cleanName,
			"add":  server,
			"port": port,
			"id":   uuid,
			"aid":  getInt(p["alterId"]),
			"scy":  cipher,
			"net":  network,
			"type": "none",
			"host": "",
			"path": "",
			"tls":  "",
			"sni":  "",
		}

		if getBool(p["tls"]) {
			vj["tls"] = "tls"
			if sni := getString(p["servername"]); sni != "" {
				vj["sni"] = sni
			}
		}

		if network == "ws" {
			if wsOpts, ok := p["ws-opts"].(map[string]interface{}); ok {
				if path := getString(wsOpts["path"]); path != "" {
					vj["path"] = path
				}
				if headers, ok := wsOpts["headers"].(map[string]interface{}); ok {
					if host := getString(headers["Host"]); host != "" {
						vj["host"] = host
					}
				}
			}
		} else if network == "grpc" {
			if grpcOpts, ok := p["grpc-opts"].(map[string]interface{}); ok {
				if svc := getString(grpcOpts["grpc-service-name"]); svc != "" {
					vj["path"] = svc
				}
			}
		}

		jsonBytes, _ := json.Marshal(vj)
		return "vmess://" + base64.StdEncoding.EncodeToString(jsonBytes)

	case "vless":
		uuid := getString(p["uuid"])
		network := getString(p["network"])

		link := fmt.Sprintf("vless://%s@%s:%d?encryption=none", uuid, server, port)

		if network != "" {
			link += "&type=" + network
		}

		if getBool(p["tls"]) {
			link += "&security=tls"
			if sni := getString(p["servername"]); sni != "" {
				link += "&sni=" + sni
			}
		}

		if ropts, ok := p["reality-opts"].(map[string]interface{}); ok {
			link += "&security=reality"
			if pbk := getString(ropts["public-key"]); pbk != "" {
				link += "&pbk=" + pbk
			}
			if sid := getString(ropts["short-id"]); sid != "" {
				link += "&sid=" + sid
			}
			if fp := getString(p["client-fingerprint"]); fp != "" {
				link += "&fp=" + fp
			}
		}

		return link + "#" + url.PathEscape(cleanName)

	default:
		// 暂不支持或不需要的协议
		return ""
	}
}

// ------------------- [安全类型断言助手] -------------------

func getInt(v interface{}) int {
	switch val := v.(type) {
	case int:
		return val
	case float64:
		return int(val)
	case int64:
		return int(val)
	default:
		return 0
	}
}

func getString(v interface{}) string {
	if val, ok := v.(string); ok {
		return val
	}
	return ""
}

func getBool(v interface{}) bool {
	if val, ok := v.(bool); ok {
		return val
	}
	return false
}
