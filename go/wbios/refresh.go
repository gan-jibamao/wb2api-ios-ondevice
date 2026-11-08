package wbios

import (
	"errors"
	"fmt"
	"strings"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/upstream"
)

// 测试可覆盖的上游 base（生产留空，走 upstream 默认）。
// 抽成变量而不是常量，是为了让单测能用 httptest 顶掉真实端点——
// 换票会消耗 RT 轮换额度，测试绝不能打真网络。
var (
	importChatBaseCN     string
	importChatBaseGlobal string
)

// refreshAccount 用 refreshToken 换新票。
//
// 复用 internal/upstream.RefreshToken，不另写协议：请求头（X-Refresh-Token、
// X-Auth-Refresh-Source: plugin、X-CodeBuddy-Request 等风控头）、30s I/O 超时、
// expiresIn 十年上限、以及「AT/RT 同时轮换」的写回语义，都与网关运行时完全一致。
//
// 返回的 *auth.Auth 已带新 AccessToken / RefreshToken / ExpiresAt。
// 注意：服务端在换票时就把旧 refreshToken 作废了，调用方必须立刻落盘。
func refreshAccount(uid, rt, domain, realm, enterpriseID string) (*auth.Auth, error) {
	if strings.TrimSpace(rt) == "" {
		return nil, fmt.Errorf("refreshToken 为空")
	}
	c := upstream.New()
	c.GlobalEnabled = true // 否则 global 账号会打到 CN 端点
	if importChatBaseCN != "" {
		c.ChatBaseCN = importChatBaseCN
	}
	if importChatBaseGlobal != "" {
		c.ChatBaseGlobal = importChatBaseGlobal
	}

	a := auth.NewForRefresh(uid, rt, domain, realm, enterpriseID)
	if err := c.RefreshToken(a); err != nil {
		// 把上游的 Error 翻成人话：401/403 基本就是 RT 废了或格式不对，
		// 原始 body 塞给用户看没意义。
		var uerr *upstream.Error
		if errors.As(err, &uerr) {
			if uerr.Status == 401 || uerr.Status == 403 {
				return nil, fmt.Errorf("refreshToken 已失效或格式错误（HTTP %d）", uerr.Status)
			}
			return nil, fmt.Errorf("HTTP %d：%s", uerr.Status, uerr.Msg)
		}
		return nil, err
	}
	if strings.TrimSpace(a.AccessToken) == "" {
		return nil, fmt.Errorf("换票返回空 accessToken")
	}
	return a, nil
}
