package service

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"nodectl/internal/database"
	"nodectl/internal/logger"

	"gopkg.in/yaml.v3"
)

const (
	MihomoApiURL      = "https://api.github.com/repos/MetaCubeX/mihomo/releases/latest"
	MihomoDBConfigKey = "mihomo_core_version"
)

var GlobalMihomo *MihomoService

type MihomoService struct {
	mu      sync.RWMutex
	dirPath string // 存放核心的目录: data/mihomo
	binPath string // 二进制文件的最终路径
}

// InitMihomo 初始化核心管理器
func InitMihomo() {
	dir := filepath.Join("data", "mihomo")
	if err := os.MkdirAll(dir, 0755); err != nil {
		logger.Log.Error("创建 Mihomo 核心目录失败", "err", err)
		return
	}

	binName := "mihomo"
	if runtime.GOOS == "windows" {
		binName = "mihomo.exe"
	}

	GlobalMihomo = &MihomoService{
		dirPath: dir,
		binPath: filepath.Join(dir, binName),
	}

	// 检查核心是否就绪
	if !GlobalMihomo.IsCoreReady() {
		logger.Log.Warn("本地暂无 Mihomo 核心，正在启动后台自动下载...")
		// 开启一个后台协程静默下载，不阻塞主程序启动
		go func() {
			if err := GlobalMihomo.ForceUpdate(); err != nil {
				logger.Log.Error("Mihomo 核心启动时自动下载失败", "error", err)
			} else {
				logger.Log.Info("Mihomo 核心自动下载部署完成！前端测速功能已就绪。")
			}
		}()
	} else {
		logger.Log.Debug("Mihomo 测试核心已就绪", "version", GlobalMihomo.GetLocalVersion())
	}
}

// IsCoreReady 检查物理文件是否存在
func (s *MihomoService) IsCoreReady() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, err := os.Stat(s.binPath)
	return err == nil
}

// GetLocalVersion 读取本地数据库记录的版本号
func (s *MihomoService) GetLocalVersion() string {
	if !s.IsCoreReady() {
		return "" // 文件不在，直接视为无版本
	}
	var config database.SysConfig
	if err := database.DB.Where("key = ?", MihomoDBConfigKey).First(&config).Error; err != nil {
		return ""
	}
	return config.Value
}

// GetRemoteVersion 调用 GitHub API 获取最新版本和下载链接 (适配各种架构)
func (s *MihomoService) GetRemoteVersion() (version string, downloadURL string, isZip bool, err error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, _ := http.NewRequest("GET", MihomoApiURL, nil)
	req.Header.Set("User-Agent", "NodeCTL-Core-Manager")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", false, fmt.Errorf("GitHub API 错误: %s", resp.Status)
	}

	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", "", false, err
	}

	// 利用 Go 内置 runtime 动态匹配当前系统
	keyword := fmt.Sprintf("mihomo-%s-%s", runtime.GOOS, runtime.GOARCH)

	for _, asset := range release.Assets {
		// 排除 alpha 测试版
		if strings.Contains(asset.Name, keyword) && !strings.Contains(asset.Name, "alpha") {
			if strings.HasSuffix(asset.Name, ".gz") {
				return release.TagName, asset.BrowserDownloadURL, false, nil
			} else if strings.HasSuffix(asset.Name, ".zip") {
				return release.TagName, asset.BrowserDownloadURL, true, nil
			}
		}
	}

	return "", "", false, errors.New("未找到匹配当前系统架构的 Mihomo 核心文件")
}

// ForceUpdate 强制下载并更新核心
func (s *MihomoService) ForceUpdate() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	version, dlURL, isZip, err := s.GetRemoteVersion()
	if err != nil {
		return fmt.Errorf("获取远程版本失败: %w", err)
	}

	tempArchive := filepath.Join(s.dirPath, "temp_archive")

	// 复用与 geo 一致的通用下载策略
	if err := downloadFile(tempArchive, dlURL); err != nil {
		return fmt.Errorf("文件下载失败: %w", err)
	}
	defer os.Remove(tempArchive)

	tempBin := s.binPath + ".tmp"
	if isZip {
		if err := s.extractZip(tempArchive, tempBin); err != nil {
			return err
		}
	} else {
		if err := s.extractGz(tempArchive, tempBin); err != nil {
			return err
		}
	}

	// Linux/Mac 赋予执行权限
	if runtime.GOOS != "windows" {
		os.Chmod(tempBin, 0755)
	}

	os.Remove(s.binPath)
	if err := os.Rename(tempBin, s.binPath); err != nil {
		return fmt.Errorf("替换文件失败: %w", err)
	}

	// 写入数据库
	database.DB.Model(&database.SysConfig{}).Where("key = ?", MihomoDBConfigKey).Update("value", version)

	logger.Log.Info("Mihomo 核心更新成功", "version", version)
	return nil
}

// extractGz 解压 .gz (Linux/Mac)
func (s *MihomoService) extractGz(gzFile, destFile string) error {
	f, err := os.Open(gzFile)
	if err != nil {
		return err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gr.Close()

	out, err := os.Create(destFile)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, gr)
	return err
}

// extractZip 解压 .zip (Windows)
func (s *MihomoService) extractZip(zipFile, destFile string) error {
	r, err := zip.OpenReader(zipFile)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		if strings.Contains(f.Name, "mihomo") {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			out, err := os.Create(destFile)
			if err != nil {
				rc.Close()
				return err
			}
			_, err = io.Copy(out, rc)
			out.Close()
			rc.Close()
			if err == nil {
				return nil
			}
		}
	}
	return errors.New("zip 压缩包中未找到核心文件")
}

// TestNodeInfo 用于向 Mihomo 传递需要测试的节点信息
type TestNodeInfo struct {
	Name string
	Link string
}

// MinimalConfig Mihomo 极简测试配置结构
type MinimalConfig struct {
	MixedPort          int           `yaml:"mixed-port"`
	ExternalController string        `yaml:"external-controller"`
	AllowLan           bool          `yaml:"allow-lan"`
	Mode               string        `yaml:"mode"`
	LogLevel           string        `yaml:"log-level"`
	Proxies            []interface{} `yaml:"proxies"`
}

// GenerateTestConfig 动态生成 Mihomo 测速所需的临时 yaml 文件
// 接收一个节点数组，支持单节点或多节点批量测试
func (s *MihomoService) GenerateTestConfig(nodes []TestNodeInfo) (yamlPath string, apiPort int, mixedPort int, err error) {
	if len(nodes) == 0 {
		return "", 0, 0, errors.New("没有需要测试的节点")
	}

	// 1. 获取两个系统的随机空闲端口，避免与系统其他服务冲突
	apiPort, err = getFreePort()
	if err != nil {
		return "", 0, 0, fmt.Errorf("分配 API 端口失败: %w", err)
	}
	mixedPort, err = getFreePort()
	if err != nil {
		return "", 0, 0, fmt.Errorf("分配代理端口失败: %w", err)
	}

	// 2. 组装极简基础配置
	config := MinimalConfig{
		MixedPort:          mixedPort,
		ExternalController: fmt.Sprintf("127.0.0.1:%d", apiPort),
		AllowLan:           false,
		Mode:               "global",
		LogLevel:           "error",
		Proxies:            make([]interface{}, 0), // 初始化为空接口数组
	}

	// 3. 复用 links.go 解析逻辑，将数据库 Link 转为 Mihomo 节点
	for _, n := range nodes {
		clashNode := ParseLinkToClashNode(n.Link, "")
		if clashNode != nil {
			if clashNode.Server == "" || clashNode.Port <= 0 || clashNode.Port > 65535 {
				logger.Log.Warn("测试节点基础参数缺失已拦截", "name", n.Name)
				continue
			}

			clashNode.Name = n.Name

			// AlterId 已改为 *int，yaml.Marshal 可正确序列化 alterId:0；
			// 此处仅保留 cipher 兜底修复。
			nodeMap := make(map[string]interface{})
			nodeBytes, _ := yaml.Marshal(clashNode)
			yaml.Unmarshal(nodeBytes, &nodeMap) // 先转成通用的 map

			if clashNode.Type == "vmess" {
				// 防止因为 omitempty 导致 cipher 丢失，引起引擎 Parse config error
				if clashNode.Cipher == "" {
					nodeMap["cipher"] = "auto"
				}
			}

			config.Proxies = append(config.Proxies, nodeMap)
		} else {
			logger.Log.Warn("测试节点解析失败跳过", "name", n.Name, "link", n.Link)
		}
	}

	if len(config.Proxies) == 0 {
		return "", 0, 0, errors.New("所有节点解析均失败，无法生成测试配置")
	}

	// 4. 创建随机的临时配置文件 (支持高并发，不同用户同时测速不会冲突)
	tempFile, err := os.CreateTemp(s.dirPath, "test_*.yaml")
	if err != nil {
		return "", 0, 0, fmt.Errorf("创建临时配置文件失败: %w", err)
	}
	defer tempFile.Close()

	// 5. 序列化为 YAML 并写入
	encoder := yaml.NewEncoder(tempFile)
	encoder.SetIndent(2)
	if err := encoder.Encode(&config); err != nil {
		os.Remove(tempFile.Name()) // 出错时清理残骸
		return "", 0, 0, fmt.Errorf("写入 YAML 失败: %w", err)
	}
	encoder.Close()

	return tempFile.Name(), apiPort, mixedPort, nil
}

// getFreePort 向操作系统申请一个可用的随机 TCP 端口
func getFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// SpeedTestResult 定义流式返回的数据结构
type SpeedTestResult struct {
	NodeID string `json:"node_id"`
	Type   string `json:"type"` // "ping" | "tcp" | "speed" | "error"
	Text   string `json:"text"` // 界面显示的文字，如 "45ms", "5.2 MB/s"
}

func NormalizeSpeedTestMode(mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case "ping_only", "all", "ping_speed":
		return strings.TrimSpace(strings.ToLower(mode))
	default:
		return "ping_speed"
	}
}

// RunBatchTestWithMode 流水线逐个启动核心并进行测速 (支持按请求指定模式)
func (s *MihomoService) RunBatchTestWithMode(ctx context.Context, nodes []database.AirportNode, resultChan chan<- SpeedTestResult, mode string) {
	defer close(resultChan) // 退出时自动关闭通道，前端 SSE 连接结束

	testMode := NormalizeSpeedTestMode(mode)

	// 开始流水线排队测试
	for _, n := range nodes {
		// 每次测试前检查用户是否停止了测试，判断逻辑是用户关闭弹窗或者手动停止。如果是则立刻中断后续所有测试
		select {
		case <-ctx.Done():
			return
		default:
		}

		// 使用匿名函数包装单节点测试逻辑，利用 defer 确保哪怕崩溃也能完美清理当前节点的文件和进程
		func(node database.AirportNode) {
			// 1. 为【这一个节点】单独生成极简配置
			testInfos := []TestNodeInfo{{Name: node.Name, Link: node.Link}}
			yamlPath, apiPort, mixedPort, err := s.GenerateTestConfig(testInfos)
			if err != nil {
				// 配置残缺，直接把错误推送到【当前节点】的标签上，不再引发全局 Alert
				resultChan <- SpeedTestResult{NodeID: node.ID, Type: "error", Text: "配置残缺"}
				return
			}
			defer os.Remove(yamlPath) // 测完即焚

			// 2. 为【这一个节点】拉起 Mihomo 进程
			cmd := exec.CommandContext(ctx, s.binPath, "-d", s.dirPath, "-f", yamlPath)

			var outBuf bytes.Buffer
			cmd.Stdout = &outBuf
			cmd.Stderr = &outBuf

			if err := cmd.Start(); err != nil {
				resultChan <- SpeedTestResult{NodeID: node.ID, Type: "error", Text: "引擎拉起失败"}
				return
			}

			// 确保测完彻底杀掉进程
			defer func() {
				if cmd.Process != nil {
					cmd.Process.Kill()
				}
			}()

			// 3. 监控当前引擎是否秒退
			processExit := make(chan error, 1)
			go func() {
				processExit <- cmd.Wait()
			}()

			client := &http.Client{Timeout: 2 * time.Second}
			ready := false
			crashed := false

			for i := 0; i < 75; i++ {
				select {
				case <-processExit:
					crashed = true
					errMsg := "引擎异常"
					// 精准捕获 Mihomo 抛出的不兼容错误
					if strings.Contains(outBuf.String(), "Parse config error") || strings.Contains(outBuf.String(), "unmarshal errors") {
						errMsg = "协议不兼容"
					}
					logger.Log.Warn("单节点引擎拦截", "node", node.Name, "error", outBuf.String())

					// 将错误发送给指定 NodeID，前端会自动渲染为红色的 Tag，而不是弹出 Alert
					resultChan <- SpeedTestResult{NodeID: node.ID, Type: "error", Text: errMsg}
				default:
				}

				if crashed {
					break // 引擎已崩，跳出等待
				}

				resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/version", apiPort))
				if err == nil && resp.StatusCode == 200 {
					ready = true
					resp.Body.Close()
					break
				}
				time.Sleep(200 * time.Millisecond)
			}

			if crashed {
				return // 错误已发送，直接进入下一个节点的测试
			}

			if !ready {
				resultChan <- SpeedTestResult{NodeID: node.ID, Type: "error", Text: "启动超时"}
				return
			}

			// 4. 引擎就绪，开始执行实际的测速逻辑...
			proxyURL, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", mixedPort))
			proxyTransport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}

			// --- 步骤 A: 基础测延迟 (Ping) ---
			delayURL := fmt.Sprintf("http://127.0.0.1:%d/proxies/%s/delay?timeout=5000&url=http://cp.cloudflare.com/generate_204", apiPort, url.PathEscape(node.Name))

			var totalPing int
			var pingSuccess int

			// 循环测试 3 次，取成功次数的平均值
			for i := 0; i < 3; i++ {
				resp, err := client.Get(delayURL)
				if err == nil {
					var delayRes struct {
						Delay int `json:"delay"`
					}
					// 解析 JSON 并确认 Delay > 0
					if decodeErr := json.NewDecoder(resp.Body).Decode(&delayRes); decodeErr == nil && delayRes.Delay > 0 {
						totalPing += delayRes.Delay
						pingSuccess++
					}
					resp.Body.Close() // 循环内必须手动关闭 Body 释放连接
				}
				// 每次请求间隔 100ms，防止被目标服务器限流
				if i < 2 {
					time.Sleep(100 * time.Millisecond)
				}
			}

			// === 核心修改区：引入 TCP Fallback 兜底逻辑 ===
			var totalTCPDelay int64
			var tcpSuccess int

			// 触发 TCP 测试的两个条件：
			// 1. testMode == "all" (用户本来就要求完整测试 TCP 延迟)
			// 2. pingSuccess == 0 (Ping不通，需要用 TCP Fallback 验证节点是否真死了)
			if testMode == "all" || pingSuccess == 0 {
				tcpClient := &http.Client{
					Transport: proxyTransport,
					Timeout:   3 * time.Second,
				}
				// 循环测试 3 次
				for i := 0; i < 3; i++ {
					reqTCP, _ := http.NewRequest("HEAD", "http://1.1.1.1", nil)
					reqTCP.Close = true // 强制禁止 Keep-Alive，保证每次都是真实握手

					startTCP := time.Now()
					tcpResp, err := tcpClient.Do(reqTCP)
					if err == nil {
						tcpResp.Body.Close() // 循环内手动关闭
						totalTCPDelay += time.Since(startTCP).Milliseconds()
						tcpSuccess++
					}
					if i < 2 {
						time.Sleep(100 * time.Millisecond)
					}
				}
			}

			var avgTCPDelay int64
			if tcpSuccess > 0 {
				avgTCPDelay = totalTCPDelay / int64(tcpSuccess)
			}

			if pingSuccess > 0 {
				// 存活情况 1：常规 Ping 正常通车
				avgPing := totalPing / pingSuccess
				resultChan <- SpeedTestResult{NodeID: node.ID, Type: "ping", Text: fmt.Sprintf("%d ms", avgPing)}
			} else if tcpSuccess > 0 {
				// 存活情况 2：Ping 阵亡，但 TCP 握手存活 (Fallback 成功)
				resultChan <- SpeedTestResult{NodeID: node.ID, Type: "ping", Text: fmt.Sprintf("TCP %dms", avgTCPDelay)}
			} else {
				// 绝望情况：双双阵亡，正式宣告死刑
				resultChan <- SpeedTestResult{NodeID: node.ID, Type: "error", Text: "无效节点"}
				return // 彻底终止该节点的后续带宽测速
			}

			if testMode == "ping_only" {
				return
			}

			// 如果是 all 模式，且既然活到了这里，正常填补前端 tcp 列的数据
			if testMode == "all" {
				if tcpSuccess > 0 {
					resultChan <- SpeedTestResult{NodeID: node.ID, Type: "tcp", Text: fmt.Sprintf("%d ms", avgTCPDelay)}
				} else {
					// 走到这里的极端边缘情况：Ping 通了，但 TCP 却死了。不适合继续测速。
					resultChan <- SpeedTestResult{NodeID: node.ID, Type: "error", Text: "TCP异常"}
					return
				}
			}

			// --- 步骤 C: 测真实带宽 (通过 Mihomo /traffic 接口获取内核级真实峰值) ---
			speedClient := &http.Client{
				Transport: proxyTransport,
				Timeout:   8 * time.Second, // 延长到 8 秒
			}

			// 动态获取用户设置的测速文件大小 (默认 50MB)
			var fileSizeConfig database.SysConfig
			database.DB.Where("key = ?", "pref_speed_test_file_size").First(&fileSizeConfig)
			fileSizeMB := "50"
			if fileSizeConfig.Value != "" {
				fileSizeMB = fileSizeConfig.Value
			}

			// 将 MB 转换为所需的格式
			bytesSize := "50000000"
			if mb, err := strconv.Atoi(fileSizeMB); err == nil && mb > 0 {
				bytesSize = fmt.Sprintf("%d", mb*1000000)
			}

			// 1. 建立备用测速池：提升测速包体积，拉长整个连接过程给足充分时间去冲高并发流量
			speedURLs := []string{
				fmt.Sprintf("https://speed.cloudflare.com/__down?bytes=%s", bytesSize),
				fmt.Sprintf("http://speedtest.tele2.net/%sMB.zip", fileSizeMB),
				fmt.Sprintf("https://proof.ovh.net/files/%sMb.dat", fileSizeMB),
			}

			var finalSpeedMBps float64
			var speedSuccess bool
			var interceptCode int // 专门记录是否真的被拉黑
			var speedMu sync.Mutex

			// 启动独立的 traffic 监听协程
			ctxTraffic, cancelTraffic := context.WithCancel(context.Background())
			defer cancelTraffic()

			go func() {
				trafficReq, err := http.NewRequestWithContext(ctxTraffic, "GET", fmt.Sprintf("http://127.0.0.1:%d/traffic", apiPort), nil)
				if err != nil {
					return
				}
				trafficClient := &http.Client{Timeout: 0}
				trafficResp, err := trafficClient.Do(trafficReq)
				if err != nil {
					return
				}
				defer trafficResp.Body.Close()

				decoder := json.NewDecoder(trafficResp.Body)
				for {
					var traffic struct {
						Up   int64 `json:"up"`
						Down int64 `json:"down"`
					}
					if err := decoder.Decode(&traffic); err != nil {
						break
					}
					// Mihomo traffic 接口返回的是每秒的 Byte 数 (B/s)
					currentMBps := float64(traffic.Down) / 1024 / 1024

					speedMu.Lock()
					if currentMBps > finalSpeedMBps {
						finalSpeedMBps = currentMBps
					}
					speedMu.Unlock()
				}
			}()

			for _, targetURL := range speedURLs {
				reqSpeed, _ := http.NewRequest("GET", targetURL, nil)
				reqSpeed.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
				reqSpeed.Header.Set("Cache-Control", "no-cache")

				speedResp, err := speedClient.Do(reqSpeed)
				if err != nil {
					continue
				}

				statusCode := speedResp.StatusCode
				if statusCode == http.StatusOK {
					startSpeed := time.Now()

					maxTestTimer := time.AfterFunc(4*time.Second, func() {
						speedResp.Body.Close()
					})

					buf := make([]byte, 256*1024)
					written, _ := io.CopyBuffer(io.Discard, speedResp.Body, buf)

					maxTestTimer.Stop()
					duration := time.Since(startSpeed).Seconds()
					speedResp.Body.Close()

					time.Sleep(200 * time.Millisecond)

					speedMu.Lock()
					currentMax := finalSpeedMBps
					speedMu.Unlock()

					// 兜底机制：如果没能捕捉到 traffic峰值（网速过快或测速极其短暂），但确实下载到了大量数据
					if currentMax == 0 && duration > 0 && written > 1024 {
						speedMu.Lock()
						finalSpeedMBps = (float64(written) / 1024 / 1024) / duration
						speedMu.Unlock()
					}

					// 只要从 traffic 接口采集到了峰值速度，或者下载了足够的数据，就认为测速成功
					if currentMax > 0 || written > 1024 {
						speedSuccess = true
						break // 只要有一个源测速成功，立刻跳出循环
					}
				} else {
					speedResp.Body.Close()
					if statusCode == 429 || statusCode == 403 {
						interceptCode = statusCode
					}
				}
			}

			// 综合裁决输出
			if speedSuccess {
				resultChan <- SpeedTestResult{NodeID: node.ID, Type: "speed", Text: fmt.Sprintf("%.2f MB/s", finalSpeedMBps)}
			} else if interceptCode > 0 {
				resultChan <- SpeedTestResult{NodeID: node.ID, Type: "error", Text: fmt.Sprintf("节点拦截(%d)", interceptCode)}
			} else {
				resultChan <- SpeedTestResult{NodeID: node.ID, Type: "error", Text: "测速超时"}
			}

			// 4. 温和测速：当前节点测试完毕后，强制休眠 300 毫秒
			time.Sleep(300 * time.Millisecond)
		}(n) // 将当前节点传入匿名函数执行
	}
}
