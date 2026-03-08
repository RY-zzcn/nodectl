package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"nodectl/internal/database"
	"nodectl/internal/logger"
	"nodectl/internal/service"
)

// ------------------- [系统运维 API] -------------------

// apiUpdateGeoIP 触发更新 GeoIP 数据库
func apiUpdateGeoIP(w http.ResponseWriter, r *http.Request) {
	clientIP := r.RemoteAddr
	reqPath := r.URL.Path

	if r.Method != http.MethodPost {
		logger.Log.Warn("非法请求方法", "method", r.Method, "ip", clientIP, "path", reqPath)
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	logger.Log.Info("接收到触发更新 GeoIP 数据库的请求", "ip", clientIP, "path", reqPath)
	go func() {
		logger.Log.Info("后台线程开始更新 GeoIP 数据库...")
		if err := service.GlobalGeoIP.ForceUpdate(); err != nil {
			logger.Log.Error("GeoIP 数据库更新失败", "error", err)
		} else {
			logger.Log.Info("GeoIP 数据库更新流程圆满完成")
		}
	}()

	sendJSON(w, "success", "更新任务已在后台启动，请留意日志或稍后刷新")
}

func apiGetGeoStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	localVersion := service.GlobalGeoIP.GetLocalVersion()
	remoteVersion, errRemote := service.GlobalGeoIP.GetRemoteVersion()

	status := "unknown"
	msg := ""

	// 字符串精确比对逻辑
	if localVersion == "" {
		status = "not_found" // 数据库没有记录，视为未下载
	} else if errRemote == nil && remoteVersion != "" && remoteVersion != localVersion {
		status = "update_available" // 版本字符串不一致，提示更新
	} else if errRemote == nil && remoteVersion == localVersion {
		status = "latest" // 完全一致，已是最新
	} else {
		status = "check_failed"
	}

	resp := map[string]interface{}{
		"status": "success",
		"data": map[string]interface{}{
			"local_version":  localVersion,
			"remote_version": remoteVersion,
			"state":          status,
			"error":          msg,
		},
	}

	if errRemote != nil {
		resp["data"].(map[string]interface{})["remote_error"] = errRemote.Error()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// apiGetSystemMonitor 获取系统运行状态与硬件监控数据
func apiGetSystemMonitor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// 读取 Go 底层内存状态
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// 计算运行时长
	uptime := time.Since(AppStartTime)
	days := int(uptime.Hours() / 24)
	hours := int(uptime.Hours()) % 24
	minutes := int(uptime.Minutes()) % 60
	seconds := int(uptime.Seconds()) % 60

	uptimeStr := ""
	if days > 0 {
		uptimeStr += fmt.Sprintf("%d天 ", days)
	}
	uptimeStr += fmt.Sprintf("%d时%d分%d秒", hours, minutes, seconds)

	// 组装监控数据
	data := map[string]interface{}{
		"os_arch":       fmt.Sprintf("%s / %s", runtime.GOOS, runtime.GOARCH), // 系统和架构
		"go_version":    runtime.Version(),                                    // Go版本
		"num_cpu":       runtime.NumCPU(),                                     // 逻辑CPU核心数
		"go_max_procs":  runtime.GOMAXPROCS(0),                                // 使用的线程数
		"num_goroutine": runtime.NumGoroutine(),                               // 当前协程数量
		"num_cgo_call":  runtime.NumCgoCall(),                                 // CGO调用次数
		"start_time":    AppStartTime.Format("2006/01/02 15:04:05"),           // 启动时间
		"uptime":        uptimeStr,                                            // 运行时长
		// 内存相关 (单位均为 Bytes，前端拿到后再转换为 MB/GB)
		"heap_alloc":  m.HeapAlloc,  // 当前分配的堆内存
		"heap_sys":    m.HeapSys,    // 向系统申请的堆内存
		"heap_inuse":  m.HeapInuse,  // 正在使用的堆内存
		"sys_mem":     m.Sys,        // 向系统申请的总内存
		"total_alloc": m.TotalAlloc, // 累计分配的内存(包含已释放的)
		"stack_inuse": m.StackInuse, // 栈内存使用量
		// GC 垃圾回收状态
		"num_gc":          m.NumGC,                       // 垃圾回收次数
		"pause_total_ms":  float64(m.PauseTotalNs) / 1e6, // GC总暂停时间(毫秒)
		"gc_cpu_fraction": m.GCCPUFraction,               // GC占用CPU的时间比例
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "success",
		"data":   data,
	})
}

// apiSaveCert 处理用户手动上传证书
func apiSaveCert(w http.ResponseWriter, r *http.Request) {
	// 限制上传大小为 10MB
	r.ParseMultipartForm(10 << 20)

	// 读取 .crt / .pem 文件
	fileCrt, _, err := r.FormFile("cert_file")
	if err != nil {
		sendJSON(w, "error", "缺少证书文件 (.crt/.pem)")
		return
	}
	defer fileCrt.Close()
	crtBytes, _ := io.ReadAll(fileCrt)

	// 读取 .key 文件
	fileKey, _, err := r.FormFile("key_file")
	if err != nil {
		sendJSON(w, "error", "缺少私钥文件 (.key / .pem)")
		return
	}
	defer fileKey.Close()
	keyBytes, _ := io.ReadAll(fileKey)

	// 调用 service 层的保存逻辑
	if err := service.SaveUploadedCert(crtBytes, keyBytes); err != nil {
		logger.Log.Error("保存证书失败", "error", err)
		sendJSON(w, "error", "证书保存失败: "+err.Error())
		return
	}

	sendJSON(w, "success", "证书已更新，请重启程序以生效")
}

// apiApplyCert 处理 Cloudflare 自动申请证书请求
func apiApplyCert(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email  string `json:"email"`
		ApiKey string `json:"api_key"`
		Domain string `json:"domain"`
	}
	// 解析 JSON body
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, "error", "参数解析失败")
		return
	}

	if req.ApiKey == "********" {
		var keyConf database.SysConfig
		database.DB.Where("key = ?", "cf_api_key").First(&keyConf)
		req.ApiKey = keyConf.Value
	}

	if req.Email == "" || req.ApiKey == "" || req.Domain == "" {
		sendJSON(w, "error", "请填写完整的 Cloudflare 信息")
		return
	}

	// 调用 service 层的申请逻辑
	if err := service.ApplyCloudflareCert(req.Email, req.ApiKey, req.Domain); err != nil {
		logger.Log.Error("证书申请失败", "error", err)
		sendJSON(w, "error", "申请失败: "+err.Error())
		return
	}

	sendJSON(w, "success", "证书申请任务已提交")
}

// apiRestartCore 处理前端下发的重启核心请求
// 功能：返回成功响应后，异步触发系统的热重启逻辑
func apiRestartCore(w http.ResponseWriter, r *http.Request) {
	clientIP := r.RemoteAddr
	reqPath := r.URL.Path

	if r.Method != http.MethodPost {
		logger.Log.Warn("非法请求方法", "method", r.Method, "ip", clientIP, "path", reqPath)
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	logger.Log.Info("接收到面板核心重启请求", "ip", clientIP)

	// 必须先给前端返回成功的 JSON，否则直接重启会导致前端请求 Pending 并报错
	sendJSON(w, "success", "系统核心即将重启，面板可能会短暂断开连接...")

	// 延迟 1 秒后触发重启，确保 HTTP 响应已经发送给前端
	go func() {
		time.Sleep(1 * time.Second)
		TriggerRestart() // 调用 server.go 中的重启触发器
	}()
}

// apiGetCertLogs 获取证书申请的实时日志供前端黑框展示
func apiGetCertLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	sendJSON(w, "success", service.GetCertLogs())
}

// apiGetRecentLogs 获取最近系统日志（含中文解读）
func apiGetRecentLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	limit := 120
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}

	logs, err := service.GetRecentLogs(limit)
	if err != nil {
		logger.Log.Error("读取最近日志失败", "error", err)
		sendJSON(w, "error", "读取日志失败，请检查日志文件是否存在")
		return
	}

	sendJSON(w, "success", map[string]interface{}{
		"logs": logs,
	})
}

// apiStreamRecentLogs 通过 SSE 持续推送最近日志（含中文解读）
func apiStreamRecentLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming Unsupported", http.StatusInternalServerError)
		return
	}

	limit := 120
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ctx := r.Context()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	lastFingerprint := ""
	sendLogs := func(force bool) {
		logs, err := service.GetRecentLogs(limit)
		if err != nil {
			payload, _ := json.Marshal(map[string]interface{}{
				"status":  "error",
				"message": "读取日志失败，请检查日志文件是否存在",
			})
			fmt.Fprintf(w, "event: logs\ndata: %s\n\n", payload)
			flusher.Flush()
			return
		}

		fingerprint := buildRecentLogFingerprint(logs)
		if !force && fingerprint == lastFingerprint {
			return
		}

		payload, _ := json.Marshal(map[string]interface{}{
			"status": "success",
			"logs":   logs,
		})
		fmt.Fprintf(w, "event: logs\ndata: %s\n\n", payload)
		flusher.Flush()
		lastFingerprint = fingerprint
	}

	sendLogs(true)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sendLogs(false)
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func buildRecentLogFingerprint(logs []service.RecentLogEntry) string {
	if len(logs) == 0 {
		return "empty"
	}
	head := logs[0]
	return fmt.Sprintf("%d|%s|%s|%s", len(logs), head.Time, head.Level, head.Raw)
}
