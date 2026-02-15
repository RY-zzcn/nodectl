package server

import (
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"time"

	"nodectl/internal/database"
	"nodectl/internal/logger"
	"nodectl/internal/middleware"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// Start 启动 HTTP 服务器
func Start(tmplFS embed.FS) {
	// 1. 预编译解析模板 (只需要解析根目录下的 html)
	tmpl := template.Must(template.ParseFS(tmplFS, "templates/*.html"))

	// 2. 登录页面路由
	http.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			tmpl.ExecuteTemplate(w, "login.html", nil)
			return
		}

		if r.Method == http.MethodPost {
			username := r.FormValue("username")
			password := r.FormValue("password")

			var userConfig database.SysConfig
			var passConfig database.SysConfig
			var secretConfig database.SysConfig

			err := database.DB.Where("key = ?", "admin_username").First(&userConfig).Error
			if errors.Is(err, gorm.ErrRecordNotFound) || userConfig.Value != username {
				tmpl.ExecuteTemplate(w, "login.html", map[string]string{"Error": "用户名或密码错误"})
				return
			}

			database.DB.Where("key = ?", "admin_password").First(&passConfig)
			err = bcrypt.CompareHashAndPassword([]byte(passConfig.Value), []byte(password))
			if err != nil {
				logger.Log.Warn("登录失败: 密码错误", "尝试用户名", username, "IP", r.RemoteAddr)
				tmpl.ExecuteTemplate(w, "login.html", map[string]string{"Error": "用户名或密码错误"})
				return
			}

			database.DB.Where("key = ?", "jwt_secret").First(&secretConfig)

			claims := jwt.MapClaims{
				"username": username,
				"exp":      time.Now().Add(24 * time.Hour).Unix(),
				"iat":      time.Now().Unix(),
			}
			token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
			tokenString, err := token.SignedString([]byte(secretConfig.Value))
			if err != nil {
				logger.Log.Error("签发 Token 失败", "err", err.Error())
				tmpl.ExecuteTemplate(w, "login.html", map[string]string{"Error": "系统内部错误"})
				return
			}

			http.SetCookie(w, &http.Cookie{
				Name:     "nodectl_token",
				Value:    tokenString,
				Path:     "/",
				HttpOnly: true,
				Secure:   false,
				MaxAge:   86400,
				Expires:  time.Now().Add(24 * time.Hour),
				SameSite: http.SameSiteLaxMode,
			})

			logger.Log.Info("管理员登录成功", "用户名", username, "IP", r.RemoteAddr)
			http.Redirect(w, r, "/", http.StatusSeeOther)
		}
	})

	// 3. 主控制台界面
	http.HandleFunc("/", middleware.Auth(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		data := map[string]interface{}{"Title": "Nodectl 总览"}
		tmpl.ExecuteTemplate(w, "index.html", data)
	}))

	// 4. 退出登录路由
	http.HandleFunc("/logout", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     "nodectl_token",
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			MaxAge:   -1,
			Expires:  time.Now().Add(-1 * time.Hour),
		})
		logger.Log.Info("管理员已安全退出", "IP", r.RemoteAddr)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})

	// 5. 🌟 修改密码的异步 API 接口 (返回 JSON)
	http.HandleFunc("/api/change-password", middleware.Auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		sendJSON := func(status, message string) {
			json.NewEncoder(w).Encode(map[string]string{"status": status, "message": message})
		}

		// 解析前端传来的 JSON 数据
		var req struct {
			OldPassword     string `json:"old_password"`
			NewPassword     string `json:"new_password"`
			ConfirmPassword string `json:"confirm_password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			sendJSON("error", "请求数据格式错误")
			return
		}

		if req.NewPassword != req.ConfirmPassword {
			sendJSON("error", "两次输入的新密码不一致")
			return
		}
		if len(req.NewPassword) < 5 {
			sendJSON("error", "新密码长度不能小于 5 位")
			return
		}

		var passConfig database.SysConfig
		if err := database.DB.Where("key = ?", "admin_password").First(&passConfig).Error; err != nil {
			sendJSON("error", "系统错误，找不到管理员账号")
			return
		}

		if err := bcrypt.CompareHashAndPassword([]byte(passConfig.Value), []byte(req.OldPassword)); err != nil {
			logger.Log.Warn("修改密码失败: 旧密码错误", "IP", r.RemoteAddr)
			sendJSON("error", "当前密码输入错误")
			return
		}

		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
		if err != nil {
			logger.Log.Error("新密码加密失败", "err", err.Error())
			sendJSON("error", "密码加密失败，请稍后重试")
			return
		}

		database.DB.Model(&database.SysConfig{}).Where("key = ?", "admin_password").Update("value", string(hashedPassword))
		logger.Log.Info("管理员密码修改成功", "IP", r.RemoteAddr)

		// 强制下线当前凭证
		http.SetCookie(w, &http.Cookie{
			Name:     "nodectl_token",
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			MaxAge:   -1,
		})

		sendJSON("success", "密码修改成功！1.5秒后将重新跳转到登录页")
	}))

	// 6. 启动服务
	port := "8080"
	logger.Log.Info("Web 服务已启动", "地址", "http://localhost:"+port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		logger.Log.Error("Web 服务异常退出", "err", err.Error())
	}
}
