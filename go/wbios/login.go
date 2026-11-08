package wbios

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"workbuddy2api/internal/auth"
)

// WorkBuddy OAuth（设备授权流程）— 从 cmd/login 移植为可被 iOS 调用的函数。
//
// 流程：
//
//	LoginStart(realm) → POST {base}/v2/plugin/auth/state?platform=CLI 取 state + authUrl
//	                    （App 用内置浏览器打开 authUrl，用户登录）
//	LoginPoll(realm)  → GET {base}/v2/plugin/auth/token?state=… 轮询；
//	                    成功后 GET /v2/plugin/login/account 取 uid/nickname，
//	                    凭证落盘 auths/workbuddy-<uid>.json（账号池自动热加载）
//
// realm：cn（copilot.tencent.com，Origin codebuddy.cn）/ global（www.workbuddy.ai）。
const (
	upstreamBaseCN     = "https://copilot.tencent.com"
	upstreamBaseGlobal = "https://www.workbuddy.ai"
	loginClientUA      = "CLI/2.63.2 CodeBuddy/2.63.2"
	originCN           = "https://www.codebuddy.cn"
	originGlobal       = "https://www.workbuddy.ai"

	realmCN     = "cn"
	realmGlobal = "global"
)

type loginState struct {
	State string `json:"state"`
	Realm string `json:"realm,omitempty"`
}

func realmEndpoints(realm string) (base, origin string) {
	if strings.EqualFold(strings.TrimSpace(realm), realmGlobal) {
		return upstreamBaseGlobal, originGlobal
	}
	return upstreamBaseCN, originCN
}

func loginCommonHeaders(origin string) func(*http.Request) {
	return func(req *http.Request) {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/plain, */*")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.Header.Set("Origin", origin)
		req.Header.Set("Referer", origin+"/")
		req.Header.Set("User-Agent", loginClientUA)
	}
}

type apiEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func loginDoJSON(client *http.Client, method, fullURL string, headers func(*http.Request), body io.Reader) (json.RawMessage, int, error) {
	req, err := http.NewRequest(method, fullURL, body)
	if err != nil {
		return nil, 0, err
	}
	if headers != nil {
		headers(req)
	} else {
		loginCommonHeaders(originCN)(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, fmt.Errorf("upstream http %d", resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("upstream redirect %d", resp.StatusCode)
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("parse failed: %w", err)
	}
	if env.Code != 0 {
		return nil, resp.StatusCode, fmt.Errorf("code=%d msg=%s", env.Code, env.Msg)
	}
	return env.Data, resp.StatusCode, nil
}

func loginStatePath(dataDir string) string {
	return filepath.Join(dataDir, "login-state.json")
}

// LoginStart 取授权 URL；state 落盘到数据目录（供 LoginPoll 校验，防混域）。
func LoginStart(dataDir, realm string) (string, error) {
	base, origin := realmEndpoints(realm)
	client := &http.Client{Timeout: 30 * time.Second}
	data, _, err := loginDoJSON(client, http.MethodPost,
		base+"/v2/plugin/auth/state?platform=CLI", loginCommonHeaders(origin), strings.NewReader("{}"))
	if err != nil {
		return "", fmt.Errorf("获取授权地址失败：%w", err)
	}
	var st struct {
		State   string `json:"state"`
		AuthURL string `json:"authUrl"`
	}
	if err := json.Unmarshal(data, &st); err != nil || st.State == "" || st.AuthURL == "" {
		return "", fmt.Errorf("上游未返回 state 或 authUrl")
	}
	raw, _ := json.Marshal(loginState{State: st.State, Realm: strings.ToLower(strings.TrimSpace(realm))})
	if err := os.WriteFile(loginStatePath(dataDir), raw, 0o600); err != nil {
		return "", fmt.Errorf("写入登录状态失败：%w", err)
	}
	return st.AuthURL, nil
}

// LoginPoll 轮询一次授权结果；未完成返回 error（调用方重试），成功后落盘并返回账号 JSON。
func LoginPoll(dataDir, realm string) (string, error) {
	raw, err := os.ReadFile(loginStatePath(dataDir))
	if err != nil {
		return "", fmt.Errorf("没有待完成的登录（请先点「添加账号」）")
	}
	var ls loginState
	if err := json.Unmarshal(raw, &ls); err != nil {
		return "", fmt.Errorf("登录状态文件损坏：%w", err)
	}
	normalized := strings.ToLower(strings.TrimSpace(realm))
	if ls.Realm != "" && normalized != "" && ls.Realm != normalized {
		return "", fmt.Errorf("版本不一致：发起时是 %s，现在轮询 %s", ls.Realm, normalized)
	}
	base, origin := realmEndpoints(realm)
	client := &http.Client{Timeout: 30 * time.Second}
	headers := loginCommonHeaders(origin)

	tokRaw, status, errTok := loginDoJSON(client, http.MethodGet,
		base+"/v2/plugin/auth/token?state="+ls.State, headers, nil)
	if errTok != nil {
		if status == 0 || status >= 500 {
			return "", fmt.Errorf("令牌端点错误：%w", errTok)
		}
		return "", fmt.Errorf("尚未完成登录，请在浏览器里完成授权")
	}
	var tok struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int64  `json:"expiresIn"`
		Domain       string `json:"domain"`
	}
	if err := json.Unmarshal(tokRaw, &tok); err != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("尚未完成登录，请在浏览器里完成授权")
	}

	var acct struct {
		UID          string `json:"uid"`
		EnterpriseID string `json:"enterpriseId"`
		Nickname     string `json:"nickname"`
	}
	acctHeaders := func(r *http.Request) {
		headers(r)
		r.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	}
	if acctRaw, _, errAcct := loginDoJSON(client, http.MethodGet,
		base+"/v2/plugin/login/account?state="+ls.State, acctHeaders, nil); errAcct == nil {
		_ = json.Unmarshal(acctRaw, &acct)
	}
	if acct.UID == "" {
		return "", fmt.Errorf("登录成功但未取到账号 uid，请重试")
	}

	// 落盘为网关可读的嵌套形（account + auth），与 login.sh 完全一致
	authFile := map[string]any{
		"account": map[string]any{
			"uid":          acct.UID,
			"enterpriseId": acct.EnterpriseID,
			"nickname":     acct.Nickname,
		},
		"auth": map[string]any{
			"accessToken":  tok.AccessToken,
			"refreshToken": tok.RefreshToken,
			"expiresAt":    time.Now().Unix() + tok.ExpiresIn,
			"domain":       tok.Domain,
			"realm":        auth.ResolveRealm(normalized, tok.Domain),
		},
	}
	dir := filepath.Join(dataDir, "auths")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	out := filepath.Join(dir, "workbuddy-"+acct.UID+".json")
	b, _ := json.MarshalIndent(authFile, "", " ")
	tmp := out + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return "", fmt.Errorf("写入凭证失败：%w", err)
	}
	if err := os.Rename(tmp, out); err != nil {
		return "", fmt.Errorf("替换凭证失败：%w", err)
	}
	_ = os.Remove(loginStatePath(dataDir))

	return fmt.Sprintf(`{"uid":%q,"nickname":%q,"realm":%q,"domain":%q,"file":%q}`,
		acct.UID, acct.Nickname, auth.ResolveRealm(normalized, tok.Domain), tok.Domain, filepath.Base(out)), nil
}

// LoginCancel 取消进行中的登录（清理 state 文件）。
func LoginCancel(dataDir string) {
	_ = os.Remove(loginStatePath(dataDir))
}

// DeleteAccount 删除某个账号凭证（uid → auths/workbuddy-<uid>.json）。
func DeleteAccount(dataDir, uid string) error {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return fmt.Errorf("uid 不能为空")
	}
	p := filepath.Join(dataDir, "auths", "workbuddy-"+uid+".json")
	if err := os.Remove(p); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("账号文件不存在")
		}
		return err
	}
	return nil
}
