package middleware

import (
	"fmt"
	"net/http"

	"nodectl/internal/database"
	"nodectl/internal/logger"

	"github.com/golang-jwt/jwt/v5"
)

// Auth 鉴权中间件，用于保护需要登录才能访问的路由
func Auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. 尝试从请求中获取名为 nodectl_token 的 Cookie
		cookie, err := r.Cookie("nodectl_token")
		if err != nil {
			// 没有带 Cookie，说明没登录，重定向到登录页
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		// 2. 从数据库获取系统加密密钥 (JWT Secret)
		var secretConfig database.SysConfig
		if err := database.DB.Where("key = ?", "jwt_secret").First(&secretConfig).Error; err != nil {
			logger.Log.Error("鉴权失败: 无法读取系统密钥")
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		// 3. 解析并校验 JWT
		tokenString := cookie.Value
		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			// 确保签名算法是我们预期的 HMAC
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return []byte(secretConfig.Value), nil
		})

		// 4. 判断校验结果
		if err != nil || !token.Valid {
			logger.Log.Warn("拦截到无效或已过期的 Token", "IP", r.RemoteAddr)
			// 清除客户端那个无效的 Cookie
			http.SetCookie(w, &http.Cookie{
				Name:   "nodectl_token",
				Value:  "",
				Path:   "/",
				MaxAge: -1, // 设置为负数代表立即删除
			})
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		// 5. 校验通过，放行给下一个处理函数！
		next.ServeHTTP(w, r)
	}
}
