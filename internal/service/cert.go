package service

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"nodectl/internal/logger"
)

var (
	CertDir     = filepath.Join("data", "cert")
	CertFile    = filepath.Join(CertDir, "server.crt")
	KeyFile     = filepath.Join(CertDir, "server.key")
	certMutex   sync.RWMutex
	currentCert *tls.Certificate
)

// CertInfo 这里的结构体用于返回给前端展示
type CertInfo struct {
	Domain string `json:"domain"`
	Expire string `json:"expire"`
	Issuer string `json:"issuer"`
	Valid  bool   `json:"valid"`
}

// InitCertManager 初始化证书目录
func InitCertManager() {
	if err := os.MkdirAll(CertDir, 0755); err != nil {
		logger.Log.Error("无法创建证书目录", "error", err)
	}
}

// GetCertificate 用于 http.Server 的 TLSConfig.GetCertificate 回调
func GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	certMutex.RLock()
	defer certMutex.RUnlock()
	return currentCert, nil
}

// LoadCertificate 从磁盘加载证书
func LoadCertificate() error {
	certMutex.Lock()
	defer certMutex.Unlock()

	if _, err := os.Stat(CertFile); os.IsNotExist(err) {
		return fmt.Errorf("证书文件不存在")
	}

	pair, err := tls.LoadX509KeyPair(CertFile, KeyFile)
	if err != nil {
		return err
	}
	currentCert = &pair
	logger.Log.Info("SSL 证书加载成功")
	return nil
}

// SaveUploadedCert 保存用户上传的证书
func SaveUploadedCert(certContent, keyContent []byte) error {
	certMutex.Lock()
	defer certMutex.Unlock()

	// 备份旧证书
	os.Rename(CertFile, CertFile+".bak")
	os.Rename(KeyFile, KeyFile+".bak")

	if err := os.WriteFile(CertFile, certContent, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(KeyFile, keyContent, 0600); err != nil {
		return err
	}

	// 尝试加载新证书进行校验
	pair, err := tls.LoadX509KeyPair(CertFile, KeyFile)
	if err != nil {
		// 校验失败，回滚
		os.Rename(CertFile+".bak", CertFile)
		os.Rename(KeyFile+".bak", KeyFile)
		return fmt.Errorf("证书格式错误或密钥不匹配，已回滚: %v", err)
	}

	currentCert = &pair
	return nil
}

// GetCurrentCertInfo 解析当前加载的证书信息，供前端展示
func GetCurrentCertInfo() CertInfo {
	certMutex.RLock()
	defer certMutex.RUnlock()

	info := CertInfo{Valid: false, Domain: "--", Expire: "--", Issuer: "--"}

	if currentCert == nil || len(currentCert.Certificate) == 0 {
		return info
	}

	// 解析 x509 证书数据
	x509Cert, err := x509.ParseCertificate(currentCert.Certificate[0])
	if err != nil {
		return info
	}

	info.Valid = true
	if len(x509Cert.DNSNames) > 0 {
		info.Domain = x509Cert.DNSNames[0]
	} else {
		info.Domain = x509Cert.Subject.CommonName
	}
	info.Expire = x509Cert.NotAfter.Format("2006-01-02 15:04:05")
	info.Issuer = x509Cert.Issuer.CommonName

	// 检查是否过期
	if time.Now().After(x509Cert.NotAfter) {
		info.Expire += " (已过期)"
		info.Valid = false
	}

	return info
}

// ApplyCloudflareCert 模拟 Cloudflare 申请逻辑
// 真正的实现需要引入 github.com/go-acme/lego/v4，为了防止你编译报错，这里先写个占位逻辑
func ApplyCloudflareCert(email, apiKey, domain string) error {
	logger.Log.Info("收到证书申请请求", "domain", domain, "email", email)

	// 这里可以集成 lego 库进行真实的 ACME 申请
	// 目前先返回一个模拟错误，提示用户去完善 lego 集成
	return fmt.Errorf("ACME 功能尚未集成 lego 库，请联系开发者完善后端逻辑")
}

// CheckRequestSecure 检查请求是否安全 (HTTPS)
func CheckRequestSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if r.Header.Get("X-Forwarded-Proto") == "https" {
		return true
	}
	return false
}
