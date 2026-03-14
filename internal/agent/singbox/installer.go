// 路径: internal/agent/singbox/installer.go
// sing-box 二进制下载/安装器
// 从 GitHub Release 下载 sing-box 并安装到工作目录
package singbox

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// DefaultBinaryPath sing-box 二进制默认安装路径
	DefaultBinaryPath = "/var/lib/nodectl-agent/sing-box"

	// SingBoxGitHubRepo GitHub Release 下载 URL 模板
	// 格式: https://github.com/SagerNet/sing-box/releases/download/v{version}/sing-box-{version}-linux-{arch}.tar.gz
	SingBoxGitHubRepo = "https://github.com/SagerNet/sing-box/releases"

	// DefaultSingBoxVersion 默认安装版本
	// anytls 协议需要 1.12.0+，不要降级到 1.11.x
	DefaultSingBoxVersion = "1.13.2"
)

// platformInfo 存储检测到的平台信息
type platformInfo struct {
	OS        string // 操作系统: linux, freebsd 等
	Arch      string // CPU 架构: amd64, arm64, armv7, 386
	LibC      string // C 库类型: glibc, musl
	Distro    string // 发行版名称: alpine, ubuntu, debian 等
	ArmVer    string // ARM 版本: v5, v6, v7 (仅 ARM 架构)
	IsMusl    bool   // 是否使用 musl libc
	RawGOARCH string // Go 原始架构标识
}

// String 返回平台信息的可读描述
func (p *platformInfo) String() string {
	libc := p.LibC
	if libc == "" {
		libc = "unknown"
	}
	distro := p.Distro
	if distro == "" {
		distro = "unknown"
	}
	return fmt.Sprintf("os=%s, arch=%s, libc=%s, distro=%s", p.OS, p.Arch, libc, distro)
}

// archSuffix 返回 sing-box Release 文件名中使用的架构+变体后缀
// 例如: "amd64", "amd64-musl", "arm64", "armv7"
func (p *platformInfo) archSuffix() string {
	suffix := p.Arch
	if p.IsMusl {
		suffix += "-musl"
	}
	return suffix
}

// Installer sing-box 安装器
type Installer struct {
	binaryPath string // sing-box 二进制安装路径
	version    string // 目标版本
}

// NewInstaller 创建安装器
func NewInstaller(binaryPath string) *Installer {
	if binaryPath == "" {
		binaryPath = DefaultBinaryPath
	}
	return &Installer{
		binaryPath: binaryPath,
		version:    DefaultSingBoxVersion,
	}
}

// SetVersion 设置目标版本
func (i *Installer) SetVersion(version string) {
	i.version = strings.TrimPrefix(version, "v")
}

// GetBinaryPath 返回二进制文件路径
func (i *Installer) GetBinaryPath() string {
	return i.binaryPath
}

// IsInstalled 检查 sing-box 是否已安装
func (i *Installer) IsInstalled() bool {
	_, err := os.Stat(i.binaryPath)
	return err == nil
}

// GetVersion 获取已安装的 sing-box 版本
func (i *Installer) GetVersion() (string, error) {
	if !i.IsInstalled() {
		return "", fmt.Errorf("sing-box 未安装")
	}

	cmd := exec.Command(i.binaryPath, "version")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("获取 sing-box 版本失败: %w", err)
	}

	// 输出格式: "sing-box version 1.x.x\n..."
	line := strings.Split(string(output), "\n")[0]
	parts := strings.Fields(line)
	if len(parts) >= 3 {
		return parts[2], nil
	}

	return strings.TrimSpace(string(output)), nil
}

// NeedUpdate 检查是否需要更新
func (i *Installer) NeedUpdate(latestVersion string) bool {
	currentVersion, err := i.GetVersion()
	if err != nil {
		return true // 获取不到版本信息，需要安装
	}

	latestVersion = strings.TrimPrefix(latestVersion, "v")
	return currentVersion != latestVersion
}

// Download 下载并安装 sing-box 二进制
func (i *Installer) Download(ctx context.Context) error {
	// 检测完整的平台信息
	platform := detectPlatform()
	log.Printf("[SingBox] 平台检测结果: %s", platform.String())

	if platform.OS != "linux" {
		return fmt.Errorf("不支持的操作系统: %s (仅支持 linux)", platform.OS)
	}
	if platform.Arch == "" {
		return fmt.Errorf("不支持的系统架构: %s", platform.RawGOARCH)
	}

	version := i.version
	if version == "" {
		version = DefaultSingBoxVersion
	}

	// 构造下载 URL
	// 格式: sing-box-{version}-linux-{archSuffix}.tar.gz
	// archSuffix 示例: amd64, amd64-musl, arm64, armv7
	archSuffix := platform.archSuffix()
	filename := fmt.Sprintf("sing-box-%s-linux-%s.tar.gz", version, archSuffix)
	downloadURL := fmt.Sprintf("%s/download/v%s/%s", SingBoxGitHubRepo, version, filename)

	log.Printf("[SingBox] 下载 sing-box v%s (arch=%s, libc=%s): %s", version, platform.Arch, platform.LibC, downloadURL)

	// 确保目标目录存在
	if dir := filepath.Dir(i.binaryPath); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("创建目录失败: %w", err)
		}
	}

	// 下载到临时文件
	tmpFile, err := os.CreateTemp("", "sing-box-*.tar.gz")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpFilePath := tmpFile.Name()

	// 下载文件
	if err := downloadFile(ctx, downloadURL, tmpFile); err != nil {
		tmpFile.Close()
		os.Remove(tmpFilePath)
		return fmt.Errorf("下载 sing-box 失败: %w", err)
	}

	// 关闭文件以确保所有数据写入磁盘
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpFilePath)
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}

	// 重新打开临时文件进行解压
	tmpFile, err = os.Open(tmpFilePath)
	if err != nil {
		os.Remove(tmpFilePath)
		return fmt.Errorf("重新打开临时文件失败: %w", err)
	}
	defer tmpFile.Close()
	defer os.Remove(tmpFilePath)

	// 从 tar.gz 中提取 sing-box 二进制
	if err := extractSingBox(tmpFile, i.binaryPath, version, archSuffix); err != nil {
		return fmt.Errorf("解压 sing-box 失败: %w", err)
	}

	// 确保文件同步到磁盘
	if err := syncFile(i.binaryPath); err != nil {
		log.Printf("[SingBox] 警告: 同步文件到磁盘失败: %v", err)
	}

	// 设置可执行权限
	if err := os.Chmod(i.binaryPath, 0755); err != nil {
		return fmt.Errorf("设置执行权限失败: %w", err)
	}

	// 验证文件
	info, err := os.Stat(i.binaryPath)
	if err != nil {
		return fmt.Errorf("验证文件失败: %w", err)
	}
	log.Printf("[SingBox] 文件大小: %d bytes, 模式: %04o", info.Size(), info.Mode())

	// 安装后立即验证二进制是否可运行
	if err := i.verifyBinaryRunnable(); err != nil {
		// 如果是 musl 环境下载了 glibc 版本，或反过来，给出明确提示
		log.Printf("[SingBox] 警告: 二进制验证失败: %v", err)
		log.Printf("[SingBox] 平台信息: %s", platform.String())

		// 清理无法运行的二进制文件
		os.Remove(i.binaryPath)
		return fmt.Errorf("sing-box 二进制无法在当前系统运行（平台: %s）: %w", platform.String(), err)
	}

	log.Printf("[SingBox] sing-box v%s 安装完成: %s", version, i.binaryPath)
	return nil
}

// syncFile 确保文件同步到磁盘（仅 Linux/Unix）
func syncFile(path string) error {
	// 尝试打开文件并调用 fsync
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// verifyBinaryRunnable 验证二进制文件是否可在当前系统运行
func (i *Installer) verifyBinaryRunnable() error {
	// 尝试执行 sing-box version 检查是否可以运行
	cmd := exec.Command(i.binaryPath, "version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("执行 '%s version' 失败: %w, 输出: %s", i.binaryPath, err, strings.TrimSpace(string(output)))
	}

	outputStr := strings.TrimSpace(string(output))
	if !strings.Contains(outputStr, "sing-box") {
		return fmt.Errorf("二进制输出异常，未包含 'sing-box' 标识: %s", outputStr)
	}

	log.Printf("[SingBox] 二进制验证通过: %s", strings.Split(outputStr, "\n")[0])
	return nil
}

// Verify 验证二进制文件完整性（执行 sing-box version 确认可运行）
func (i *Installer) Verify() error {
	if !i.IsInstalled() {
		return fmt.Errorf("sing-box 二进制不存在: %s", i.binaryPath)
	}

	version, err := i.GetVersion()
	if err != nil {
		return fmt.Errorf("sing-box 二进制验证失败: %w", err)
	}

	log.Printf("[SingBox] 验证通过: sing-box v%s", version)
	return nil
}

// EnsureInstalled 确保 sing-box 已安装，若不存在则下载
func (i *Installer) EnsureInstalled(ctx context.Context) error {
	if i.IsInstalled() {
		if err := i.Verify(); err == nil {
			return nil
		}
		log.Printf("[SingBox] 已安装的 sing-box 验证失败，重新下载")
		// 移除损坏或不兼容的二进制
		os.Remove(i.binaryPath)
	}
	return i.Download(ctx)
}

// --- 平台检测函数 ---

// detectPlatform 全面检测当前运行平台信息
// 包括: 操作系统、CPU 架构、C 库类型（glibc/musl）、发行版
func detectPlatform() *platformInfo {
	p := &platformInfo{
		OS:        runtime.GOOS,
		RawGOARCH: runtime.GOARCH,
	}

	// 检测架构
	p.Arch = mapGoArch(runtime.GOARCH)

	// 仅 Linux 平台需要检测 libc 和发行版
	if runtime.GOOS == "linux" {
		p.Distro = detectDistro()
		p.LibC = detectLibC()
		p.IsMusl = (p.LibC == "musl")

		log.Printf("[SingBox] 环境检测: distro=%s, libc=%s, arch=%s, isMusl=%v",
			p.Distro, p.LibC, p.Arch, p.IsMusl)
	}

	return p
}

// mapGoArch 将 Go 的 GOARCH 映射到 sing-box 发布文件名使用的架构标识
func mapGoArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "amd64"
	case "arm64":
		return "arm64"
	case "arm":
		return "armv7"
	case "386":
		return "386"
	default:
		return ""
	}
}

// detectDistro 检测 Linux 发行版名称
// 通过读取 /etc/os-release 文件获取发行版信息
func detectDistro() string {
	// 优先读取 /etc/os-release（几乎所有现代 Linux 发行版都有）
	data, err := os.ReadFile("/etc/os-release")
	if err == nil {
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			// 查找 ID= 字段（不是 ID_LIKE=）
			if strings.HasPrefix(line, "ID=") {
				id := strings.TrimPrefix(line, "ID=")
				id = strings.Trim(id, "\"'")
				id = strings.ToLower(id)
				if id != "" {
					return id
				}
			}
		}
	}

	// 备用方案: 检查特定发行版文件
	if _, err := os.Stat("/etc/alpine-release"); err == nil {
		return "alpine"
	}
	if _, err := os.Stat("/etc/debian_version"); err == nil {
		return "debian"
	}
	if _, err := os.Stat("/etc/redhat-release"); err == nil {
		return "rhel"
	}

	return "unknown"
}

// detectLibC 检测系统使用的 C 标准库类型（glibc 或 musl）
// 使用多种方法确保准确检测
func detectLibC() string {
	// 方法1: 检查 ldd 版本输出（最可靠）
	if libc := detectLibCByLdd(); libc != "" {
		return libc
	}

	// 方法2: 检查是否存在 musl 动态链接器
	if libc := detectLibCByMuslLinker(); libc != "" {
		return libc
	}

	// 方法3: 通过 /proc/self/maps 检查加载的库
	if libc := detectLibCByProcMaps(); libc != "" {
		return libc
	}

	// 方法4: 基于发行版推断（作为最后手段）
	if libc := detectLibCByDistro(); libc != "" {
		return libc
	}

	// 默认假设 glibc（大多数 Linux 发行版使用 glibc）
	log.Printf("[SingBox] 无法确定 C 库类型，默认使用 glibc")
	return "glibc"
}

// detectLibCByLdd 通过 ldd --version 的输出检测 C 库类型
func detectLibCByLdd() string {
	// 先检查 ldd 是否存在
	lddPath, err := exec.LookPath("ldd")
	if err != nil {
		return ""
	}

	cmd := exec.Command(lddPath, "--version")
	// ldd --version 在 musl 下会输出到 stderr，在 glibc 下输出到 stdout
	output, err := cmd.CombinedOutput()
	outputStr := strings.ToLower(string(output))

	// musl 的 ldd 输出类似: "musl libc (x86_64)\nVersion 1.2.x\n..."
	if strings.Contains(outputStr, "musl") {
		log.Printf("[SingBox] ldd 检测到 musl libc")
		return "musl"
	}

	// glibc 的 ldd 输出类似: "ldd (GNU libc) 2.xx\n..."
	// 或: "ldd (Ubuntu GLIBC 2.xx-xubuntux) 2.xx\n..."
	if strings.Contains(outputStr, "glibc") || strings.Contains(outputStr, "gnu libc") ||
		strings.Contains(outputStr, "gnu c library") || strings.Contains(outputStr, "free software foundation") {
		log.Printf("[SingBox] ldd 检测到 glibc")
		return "glibc"
	}

	// 如果 ldd --version 成功执行但没有明确标识，检查退出码
	// musl 的 ldd --version 通常返回非零退出码
	if err != nil && strings.Contains(outputStr, "musl") {
		return "musl"
	}

	return ""
}

// detectLibCByMuslLinker 检查 musl 动态链接器是否存在
func detectLibCByMuslLinker() string {
	// musl 的动态链接器路径模式
	muslLinkerPaths := []string{
		"/lib/ld-musl-x86_64.so.1",
		"/lib/ld-musl-aarch64.so.1",
		"/lib/ld-musl-armhf.so.1",
		"/lib/ld-musl-i386.so.1",
	}

	for _, path := range muslLinkerPaths {
		if _, err := os.Stat(path); err == nil {
			log.Printf("[SingBox] 检测到 musl 动态链接器: %s", path)
			return "musl"
		}
	}

	// 同时检查通用 musl 路径模式
	matches, _ := filepath.Glob("/lib/ld-musl-*.so.1")
	if len(matches) > 0 {
		log.Printf("[SingBox] 检测到 musl 动态链接器 (glob): %s", matches[0])
		return "musl"
	}

	return ""
}

// detectLibCByProcMaps 通过 /proc/self/maps 检查当前进程加载的库
func detectLibCByProcMaps() string {
	data, err := os.ReadFile("/proc/self/maps")
	if err != nil {
		return ""
	}

	content := strings.ToLower(string(data))
	if strings.Contains(content, "musl") || strings.Contains(content, "ld-musl") {
		log.Printf("[SingBox] /proc/self/maps 检测到 musl")
		return "musl"
	}
	if strings.Contains(content, "libc.so") || strings.Contains(content, "libc-") {
		// libc.so 通常表示 glibc
		log.Printf("[SingBox] /proc/self/maps 检测到 glibc (libc.so)")
		return "glibc"
	}

	return ""
}

// detectLibCByDistro 基于发行版名称推断 C 库类型（最后手段）
func detectLibCByDistro() string {
	distro := detectDistro()
	switch distro {
	case "alpine":
		// Alpine Linux 始终使用 musl
		log.Printf("[SingBox] 基于发行版 alpine 推断为 musl")
		return "musl"
	case "ubuntu", "debian", "centos", "fedora", "rhel", "rocky", "almalinux",
		"opensuse", "sles", "arch", "manjaro", "gentoo", "amzn":
		// 这些发行版默认使用 glibc
		log.Printf("[SingBox] 基于发行版 %s 推断为 glibc", distro)
		return "glibc"
	case "void":
		// Void Linux 有 glibc 和 musl 两个版本，需要进一步检查
		// 但已经走到这里说明前面的检测方法都失败了，默认 glibc
		return ""
	}
	return ""
}

// --- 内部辅助函数 ---

// downloadFile 使用 HTTP GET 下载文件
func downloadFile(ctx context.Context, url string, dst *os.File) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	client := &http.Client{
		Timeout: 5 * time.Minute, // 下载大文件给足时间
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	written, err := io.Copy(dst, resp.Body)
	if err != nil {
		return err
	}

	log.Printf("[SingBox] 下载完成: %d bytes", written)
	return nil
}

// extractSingBox 从 tar.gz 归档中提取 sing-box 二进制文件
// sing-box 发布归档结构: sing-box-{version}-linux-{archSuffix}/sing-box
func extractSingBox(archive io.Reader, destPath, version, archSuffix string) error {
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("解压 gzip 失败: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)

	// 在 tar.gz 中查找 sing-box 二进制
	// 路径格式: sing-box-{version}-linux-{archSuffix}/sing-box
	targetName := fmt.Sprintf("sing-box-%s-linux-%s/sing-box", version, archSuffix)

	log.Printf("[SingBox] 目标文件名: %s", targetName)

	var foundEntries []string
	var extracted bool

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("读取 tar 条目失败: %w", err)
		}

		// 记录所有条目用于调试
		foundEntries = append(foundEntries, fmt.Sprintf("%s (type=%d)", header.Name, header.Typeflag))

		// 精确匹配或以 /sing-box 结尾（兼容不同版本格式）
		if header.Name == targetName || strings.HasSuffix(header.Name, "/sing-box") {
			if header.Typeflag != tar.TypeReg {
				log.Printf("[SingBox] 跳过非普通文件: %s (type=%d)", header.Name, header.Typeflag)
				continue
			}

			log.Printf("[SingBox] 找到匹配条目: %s, 大小: %d bytes", header.Name, header.Size)

			// 先创建目标文件
			out, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
			if err != nil {
				return fmt.Errorf("创建目标文件失败: %w", err)
			}

			// 立即复制内容并关闭文件（不使用 defer）
			written, copyErr := io.Copy(out, tr)
			closeErr := out.Close()

			if copyErr != nil {
				return fmt.Errorf("写入二进制失败: %w", copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("关闭文件失败: %w", closeErr)
			}

			log.Printf("[SingBox] 解压完成: 写入 %d bytes 到 %s", written, destPath)
			extracted = true
			break
		}
	}

	if !extracted {
		log.Printf("[SingBox] 归档内容列表:")
		for _, entry := range foundEntries {
			log.Printf("[SingBox]   - %s", entry)
		}
		return fmt.Errorf("归档中未找到 sing-box 二进制 (查找: %s, 共 %d 个条目)", targetName, len(foundEntries))
	}

	// 验证提取的文件
	info, err := os.Stat(destPath)
	if err != nil {
		return fmt.Errorf("验证提取文件失败: %w", err)
	}

	log.Printf("[SingBox] 文件验证: %s, 大小=%d bytes, 模式=%04o", destPath, info.Size(), info.Mode())

	if info.Size() == 0 {
		return fmt.Errorf("提取的文件大小为 0，可能解压失败")
	}

	return nil
}
