package service

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ---------------------------------------------------------
// 1. 基础工具函数
// ---------------------------------------------------------

// getEmojiFlag 根据地区代码获取 Emoji 国旗
func getEmojiFlag(region string) string {
	if region == "" {
		return "🌐"
	}
	region = strings.ToUpper(strings.TrimSpace(region))
	if len(region) == 2 {
		// A 的 ASCII 码是 65，对应区域指示符 🇦 的 Unicode 是 127462
		// 偏移量 = 127462 - 65 = 127397
		const offset = 127397
		// 【修改这里】先将 byte 转为 rune，再相加
		return string(rune(region[0])+offset) + string(rune(region[1])+offset)
	}
	return region
}

// safeBase64Decode 安全的 Base64 解码，自动补齐 "=" 和处理 URL Safe 字符
func safeBase64Decode(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")
	if padding := len(s) % 4; padding > 0 {
		s += strings.Repeat("=", 4-padding)
	}
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return ""
	}
	return string(decoded)
}

// getBool 解析 URL Query 中的布尔值参数
func getBool(query url.Values, keys ...string) bool {
	for _, k := range keys {
		val := strings.ToLower(query.Get(k))
		if val == "1" || val == "true" || val == "yes" || val == "on" {
			return true
		}
	}
	return false
}

// getString 从 map[string]interface{} 安全获取字符串
func getString(m map[string]interface{}, key string) string {
	if val, ok := m[key]; ok && val != nil {
		switch v := val.(type) {
		case string:
			return v
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		}
	}
	return ""
}

// getInt 从 map[string]interface{} 安全获取整数
func getInt(m map[string]interface{}, key string) int {
	if val, ok := m[key]; ok && val != nil {
		switch v := val.(type) {
		case float64:
			return int(v)
		case string:
			if i, err := strconv.Atoi(v); err == nil {
				return i
			}
		}
	}
	return 0
}

// ---------------------------------------------------------
// 2. 协议转换统一入口
// ---------------------------------------------------------

// ParseProxyLink 解析原始链接转换为 Clash 配置字典
func ParseProxyLink(link, baseName, region string, useFlag bool) map[string]interface{} {
	link = strings.TrimSpace(link)
	if link == "" {
		return nil
	}

	// 处理国旗前缀偏好
	finalName := baseName
	if useFlag && region != "" {
		flag := getEmojiFlag(region)
		finalName = fmt.Sprintf("%s %s", flag, strings.ReplaceAll(baseName, flag, "")) // 防止重复添加
	}
	finalName = strings.TrimSpace(finalName)

	lowerLink := strings.ToLower(link)

	// VMess 特殊处理 (Base64 JSON 格式)
	if strings.HasPrefix(lowerLink, "vmess://") {
		return parseVmess(link, finalName)
	}

	// 其他标准 URL 格式协议
	if strings.HasPrefix(lowerLink, "vless://") {
		return parseVless(link, finalName)
	} else if strings.HasPrefix(lowerLink, "trojan://") {
		return parseTrojan(link, finalName)
	} else if strings.HasPrefix(lowerLink, "hy2://") || strings.HasPrefix(lowerLink, "hysteria2://") {
		return parseHysteria2(link, finalName)
	} else if strings.HasPrefix(lowerLink, "tuic://") {
		return parseTuic(link, finalName)
	} else if strings.HasPrefix(lowerLink, "ss://") {
		return parseSS(link, finalName)
	} else if strings.HasPrefix(lowerLink, "socks5://") {
		return parseSocks5(link, finalName)
	}

	return nil
}

// ---------------------------------------------------------
// 3. 各协议独立解析逻辑
// ---------------------------------------------------------

func parseVless(link, proxyName string) map[string]interface{} {
	parsed, err := url.Parse(link)
	if err != nil {
		return nil
	}

	uuid := parsed.User.Username()
	server := parsed.Hostname()
	port := parsed.Port()
	query := parsed.Query()

	network := query.Get("type")
	if network == "" {
		network = "tcp"
	}

	proxy := map[string]interface{}{
		"name":             proxyName,
		"type":             "vless",
		"server":           server,
		"port":             port,
		"uuid":             uuid,
		"network":          network,
		"udp":              true,
		"tfo":              getBool(query, "fast-open"),
		"skip-cert-verify": getBool(query, "insecure", "skip-cert-verify", "allowInsecure"),
	}

	if sni := query.Get("sni"); sni != "" {
		proxy["servername"] = sni
	}
	if flow := query.Get("flow"); flow != "" {
		proxy["flow"] = flow
	}
	if alpn := query.Get("alpn"); alpn != "" {
		proxy["alpn"] = strings.Split(alpn, ",")
	}

	// Security (Reality / TLS)
	security := query.Get("security")
	if security == "reality" {
		proxy["tls"] = true
		proxy["reality-opts"] = map[string]interface{}{
			"public-key": query.Get("pbk"),
			"short-id":   query.Get("sid"),
		}
		if fp := query.Get("fp"); fp != "" {
			proxy["client-fingerprint"] = fp
		} else {
			proxy["client-fingerprint"] = "chrome"
		}
	} else if security == "tls" || getBool(query, "tls") {
		proxy["tls"] = true
		if fp := query.Get("fp"); fp != "" {
			proxy["client-fingerprint"] = fp
		}
	}

	// Transport Options
	applyTransportOpts(proxy, network, query)
	return proxy
}

func parseTrojan(link, proxyName string) map[string]interface{} {
	parsed, err := url.Parse(link)
	if err != nil {
		return nil
	}

	password := parsed.User.Username()
	server := parsed.Hostname()
	port := parsed.Port()
	query := parsed.Query()

	network := query.Get("type")
	if network == "" {
		network = "tcp"
	}

	proxy := map[string]interface{}{
		"name":             proxyName,
		"type":             "trojan",
		"server":           server,
		"port":             port,
		"password":         password,
		"network":          network,
		"udp":              true,
		"tfo":              getBool(query, "fast-open"),
		"skip-cert-verify": getBool(query, "insecure", "skip-cert-verify"),
	}

	if sni := query.Get("sni"); sni != "" {
		proxy["sni"] = sni
	}
	if alpn := query.Get("alpn"); alpn != "" {
		proxy["alpn"] = strings.Split(alpn, ",")
	}
	if fp := query.Get("fp"); fp != "" {
		proxy["client-fingerprint"] = fp
	}

	// Security Reality
	if query.Get("security") == "reality" {
		proxy["reality-opts"] = map[string]interface{}{
			"public-key": query.Get("pbk"),
			"short-id":   query.Get("sid"),
		}
	}

	applyTransportOpts(proxy, network, query)
	return proxy
}

func parseHysteria2(link, proxyName string) map[string]interface{} {
	parsed, err := url.Parse(link)
	if err != nil {
		return nil
	}

	password := parsed.User.Username()
	server := parsed.Hostname()
	port := parsed.Port()
	query := parsed.Query()

	if password == "" {
		password = query.Get("auth") // 兼容备用参数
	}

	proxy := map[string]interface{}{
		"name":             proxyName,
		"type":             "hysteria2",
		"server":           server,
		"port":             port,
		"password":         password,
		"udp":              true,
		"skip-cert-verify": getBool(query, "insecure", "skip-cert-verify", "allowInsecure"),
	}

	sni := query.Get("sni")
	if sni == "" {
		sni = query.Get("peer")
	}
	if sni != "" {
		proxy["sni"] = sni
	}

	if alpn := query.Get("alpn"); alpn != "" {
		proxy["alpn"] = strings.Split(alpn, ",")
	}
	if obfs := query.Get("obfs"); obfs != "" {
		proxy["obfs"] = obfs
		proxy["obfs-password"] = query.Get("obfs-password")
	}
	if ports := query.Get("ports"); ports != "" {
		proxy["ports"] = ports
	}

	// 带宽参数
	up := query.Get("up")
	if up == "" {
		up = query.Get("upmbps")
	}
	if upInt, _ := strconv.Atoi(up); upInt > 0 {
		proxy["up"] = upInt
	}

	down := query.Get("down")
	if down == "" {
		down = query.Get("downmbps")
	}
	if downInt, _ := strconv.Atoi(down); downInt > 0 {
		proxy["down"] = downInt
	}

	return proxy
}

func parseTuic(link, proxyName string) map[string]interface{} {
	parsed, err := url.Parse(link)
	if err != nil {
		return nil
	}

	uuidStr := parsed.User.Username()
	password, _ := parsed.User.Password()
	server := parsed.Hostname()
	port := parsed.Port()
	query := parsed.Query()

	proxy := map[string]interface{}{
		"name":                  proxyName,
		"type":                  "tuic",
		"server":                server,
		"port":                  port,
		"uuid":                  uuidStr,
		"password":              password,
		"tls":                   true,
		"udp":                   true,
		"disable-sni":           getBool(query, "disable-sni"),
		"skip-cert-verify":      getBool(query, "insecure", "skip-cert-verify", "allowInsecure") || true, // TUIC 默认跳过
		"congestion-controller": query.Get("congestion_controller"),
		"udp-relay-mode":        query.Get("udp-relay-mode"),
		"reduce-rtt":            getBool(query, "reduce-rtt"),
		"zero-rtt":              getBool(query, "zero-rtt"),
	}

	if proxy["congestion-controller"] == "" {
		proxy["congestion-controller"] = "bbr"
	}
	if proxy["udp-relay-mode"] == "" {
		proxy["udp-relay-mode"] = "native"
	}

	if alpn := query.Get("alpn"); alpn != "" {
		proxy["alpn"] = strings.Split(alpn, ",")
	} else {
		proxy["alpn"] = []string{"h3"}
	}

	if sni := query.Get("sni"); sni != "" {
		proxy["sni"] = sni
		proxy["servername"] = sni
	}

	return proxy
}

func parseVmess(link, proxyName string) map[string]interface{} {
	// 截取 vmess:// 后的 base64
	b64Part := link[8:]
	if idx := strings.Index(b64Part, "#"); idx != -1 {
		b64Part = b64Part[:idx]
	}

	decoded := safeBase64Decode(b64Part)
	if decoded == "" {
		return nil
	}

	var v map[string]interface{}
	if err := json.Unmarshal([]byte(decoded), &v); err != nil {
		return nil
	}

	serverAddr := getString(v, "add")
	if strings.Contains(serverAddr, ":") && !strings.HasPrefix(serverAddr, "[") {
		serverAddr = "[" + serverAddr + "]" // IPv6 包裹
	}

	proxy := map[string]interface{}{
		"name":             proxyName,
		"type":             "vmess",
		"server":           serverAddr,
		"port":             getInt(v, "port"),
		"uuid":             getString(v, "id"),
		"alterId":          getInt(v, "aid"),
		"cipher":           getString(v, "scy"),
		"udp":              true,
		"skip-cert-verify": false,
		"tls":              false,
	}

	if proxy["cipher"] == "" {
		proxy["cipher"] = "auto"
	}

	// TLS 设置
	tlsVal := getString(v, "tls")
	if tlsVal != "" && strings.ToLower(tlsVal) != "none" {
		proxy["tls"] = true
		if sni := getString(v, "sni"); sni != "" {
			proxy["servername"] = sni
		}
		// 兼容各种拼写的跳过证书验证
		if v["skip-cert-verify"] == true || v["insecure"] == true || getString(v, "insecure") == "1" {
			proxy["skip-cert-verify"] = true
		}
	}

	// Network 提取
	net := getString(v, "net")
	if net == "" {
		net = "tcp"
	}
	typeField := getString(v, "type")
	if typeField == "" {
		typeField = net
	}

	// 转换为 query 形式供复用工具处理
	query := url.Values{}
	if path := getString(v, "path"); path != "" {
		query.Set("path", path)
	}
	if host := getString(v, "host"); host != "" {
		query.Set("host", host)
	} else if sni := getString(v, "sni"); sni != "" {
		query.Set("host", sni)
	}

	if net == "http" || (net == "tcp" && typeField == "http") {
		net = "http"
	}

	proxy["network"] = net
	applyTransportOpts(proxy, net, query)

	return proxy
}

func parseSS(link, proxyName string) map[string]interface{} {
	body := link[5:] // 去除 ss://
	if idx := strings.Index(body, "#"); idx != -1 {
		body = body[:idx]
	}
	if idx := strings.Index(body, "?"); idx != -1 {
		body = body[:idx]
	}

	// 如果没有 @，说明整体进行了 Base64 编码 (ss://base64(method:password@host:port))
	if !strings.Contains(body, "@") {
		if decoded := safeBase64Decode(body); decoded != "" {
			body = decoded
		}
	}

	if strings.Contains(body, "@") {
		parts := strings.SplitN(body, "@", 2)
		userInfo := parts[0]
		hostPart := parts[1]

		// 解析 userinfo (如果内部不含冒号，说明是被 Base64 编码的 method:password)
		if !strings.Contains(userInfo, ":") {
			if decodedUser := safeBase64Decode(userInfo); decodedUser != "" {
				userInfo = decodedUser
			}
		}

		if strings.Contains(userInfo, ":") {
			userParts := strings.SplitN(userInfo, ":", 2)
			method := userParts[0]
			password := userParts[1]

			lastColon := strings.LastIndex(hostPart, ":")
			if lastColon != -1 {
				server := hostPart[:lastColon]
				port := hostPart[lastColon+1:]

				if strings.Contains(server, ":") && !strings.HasPrefix(server, "[") {
					server = "[" + server + "]" // IPv6 包装
				}

				return map[string]interface{}{
					"name":     proxyName,
					"type":     "ss",
					"server":   server,
					"port":     port,
					"cipher":   method,
					"password": password,
					"udp":      true,
				}
			}
		}
	}
	return nil
}

func parseSocks5(link, proxyName string) map[string]interface{} {
	parsed, err := url.Parse(link)
	if err != nil {
		return nil
	}

	username := parsed.User.Username()
	password, _ := parsed.User.Password()
	server := parsed.Hostname()
	port := parsed.Port()
	if port == "" {
		port = "1080"
	}
	query := parsed.Query()

	proxy := map[string]interface{}{
		"name":             proxyName,
		"type":             "socks5",
		"server":           server,
		"port":             port,
		"udp":              true,
		"skip-cert-verify": getBool(query, "insecure", "skip-cert-verify"),
	}

	if username != "" {
		proxy["username"] = username
	}
	if password != "" {
		proxy["password"] = password
	}

	if getBool(query, "tls") {
		proxy["tls"] = true
		if sni := query.Get("sni"); sni != "" {
			proxy["servername"] = sni
		}
	}

	return proxy
}

// ---------------------------------------------------------
// 4. 通用传输层 (Transport) 参数挂载
// ---------------------------------------------------------
func applyTransportOpts(proxy map[string]interface{}, network string, query url.Values) {
	if network == "ws" {
		wsOpts := map[string]interface{}{
			"path":    "/",
			"headers": map[string]string{},
		}
		if p := query.Get("path"); p != "" {
			wsOpts["path"] = p
		}
		if h := query.Get("host"); h != "" {
			wsOpts["headers"].(map[string]string)["Host"] = h
		}
		proxy["ws-opts"] = wsOpts
	} else if network == "grpc" {
		grpcOpts := map[string]interface{}{
			"grpc-service-name": "",
		}
		if s := query.Get("serviceName"); s != "" {
			grpcOpts["grpc-service-name"] = s
		}
		proxy["grpc-opts"] = grpcOpts
	} else if network == "h2" {
		h2Opts := map[string]interface{}{}
		if p := query.Get("path"); p != "" {
			h2Opts["path"] = strings.Split(p, ",")
		} else {
			h2Opts["path"] = []string{"/"}
		}
		if h := query.Get("host"); h != "" {
			h2Opts["host"] = strings.Split(h, ",")
		}
		proxy["h2-opts"] = h2Opts
	} else if network == "http" {
		httpOpts := map[string]interface{}{
			"method": "GET",
		}
		if p := query.Get("path"); p != "" {
			httpOpts["path"] = strings.Split(p, ",")
		} else {
			httpOpts["path"] = []string{"/"}
		}
		if h := query.Get("host"); h != "" {
			httpOpts["headers"] = map[string]interface{}{
				"Host": strings.Split(h, ","),
			}
		}
		proxy["http-opts"] = httpOpts
	}
}
