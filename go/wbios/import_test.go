package wbios

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
)

// runImport 跑一次导入并解出 ImportResult。
func runImport(t *testing.T, dir, raw string) ImportResult {
	t.Helper()
	out, err := ImportJSON(dir, raw, false)
	if err != nil {
		t.Fatalf("ImportJSON error: %v", err)
	}
	var env struct {
		OK   bool         `json:"ok"`
		Data ImportResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("bad envelope: %v / %s", err, out)
	}
	if !env.OK {
		t.Fatalf("ok=false: %s", out)
	}
	return env.Data
}

func readAuthFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "auths", name))
	if err != nil {
		t.Fatalf("读文件失败 %s: %v", name, err)
	}
	return string(b)
}

func b64url(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// 1. 嵌套驼峰（插件 OAuth 原生 / 老大手上那份格式）
func TestImportNestedCamel(t *testing.T) {
	d := t.TempDir()
	raw := `{"account":{"uid":"u-1","nickname":"老王","enterpriseId":"e1"},
	         "auth":{"accessToken":"AT1","refreshToken":"RT1","expiresAt":1795097981,
	                 "domain":"www.codebuddy.cn","realm":"cn"}}`
	r := runImport(t, d, raw)
	if r.Added != 1 || r.Skipped != 0 || r.Total != 1 {
		t.Fatalf("added=%d skipped=%d %+v", r.Added, r.Skipped, r.Items)
	}
	body := readAuthFile(t, d, "workbuddy-u-1.json")
	if !strings.Contains(body, `"accessToken": "AT1"`) {
		t.Fatalf("落盘内容不对: %s", body)
	}
}

// 2. 嵌套蛇形 —— 反证：不归一化时 auth.Parse 必报 missing accessToken
func TestImportNestedSnake(t *testing.T) {
	// 先固化「原样喂给 auth.Parse 必失败」
	if _, err := auth.Parse([]byte(`{"account":{"uid":"u-2"},"auth":{"access_token":"AT2"}}`)); err == nil {
		t.Fatal("反证失败：auth.Parse 居然接受了蛇形")
	}
	d := t.TempDir()
	raw := `{"account":{"uid":"u-2"},"auth":{"access_token":"AT2","refresh_token":"RT2","expires_at":1795097981}}`
	r := runImport(t, d, raw)
	if r.Added != 1 {
		t.Fatalf("added=%d skipped=%d %+v", r.Added, r.Skipped, r.Items)
	}
	body := readAuthFile(t, d, "workbuddy-u-2.json")
	if !strings.Contains(body, `"accessToken": "AT2"`) {
		t.Fatalf("未归一化为驼峰: %s", body)
	}
}

// 3. 毫秒级 expires_at 归一为秒
func TestImportMillisExpiry(t *testing.T) {
	d := t.TempDir()
	raw := `[{"uid":"u-3","access_token":"AT3","expires_at":1795097981000}]`
	r := runImport(t, d, raw)
	if r.Added != 1 {
		t.Fatalf("added=%d %+v", r.Added, r.Items)
	}
	body := readAuthFile(t, d, "workbuddy-u-3.json")
	if !strings.Contains(body, `"expiresAt": 1795097981`) {
		t.Fatalf("毫秒未归一: %s", body)
	}
}

// 4. 信封 {"accounts":[…]} 一次两条
func TestImportEnvelope(t *testing.T) {
	d := t.TempDir()
	raw := `{"accounts":[{"uid":"u-4a","accessToken":"A"},{"uid":"u-4b","accessToken":"B"}]}`
	r := runImport(t, d, raw)
	if r.Added != 2 || r.Total != 2 {
		t.Fatalf("added=%d total=%d", r.Added, r.Total)
	}
}

// 5. 重复导入 = 更新，不新增；token 被覆盖为新值
func TestImportIdempotentUpdates(t *testing.T) {
	d := t.TempDir()
	if r := runImport(t, d, `{"uid":"u-5","accessToken":"AT5","refreshToken":"RT5"}`); r.Added != 1 {
		t.Fatalf("first added=%d", r.Added)
	}
	r := runImport(t, d, `{"uid":"u-5","accessToken":"AT5b","refreshToken":"RT5b"}`)
	if r.Updated != 1 || r.Added != 0 {
		t.Fatalf("second added=%d updated=%d", r.Added, r.Updated)
	}
	body := readAuthFile(t, d, "workbuddy-u-5.json")
	if !strings.Contains(body, "AT5b") {
		t.Fatalf("未覆盖为新 token: %s", body)
	}
}

// 6. 只有 refreshToken → 跳过并给明确原因，一个文件都不许写
func TestImportRefreshOnlySkipped(t *testing.T) {
	d := t.TempDir()
	raw := `{"13800000000":{"refresh_token":"RT","domain":"www.codebuddy.cn"}}`
	r := runImport(t, d, raw)
	if r.Skipped != 1 || r.Added != 0 {
		t.Fatalf("added=%d skipped=%d", r.Added, r.Skipped)
	}
	if len(r.Items) != 1 || !strings.Contains(r.Items[0].Reason, "accessToken") {
		t.Fatalf("原因不明确: %+v", r.Items)
	}
	ents, _ := os.ReadDir(filepath.Join(d, "auths"))
	if len(ents) != 0 {
		t.Fatalf("不该写文件，实际 %d 个", len(ents))
	}
}

// 7. 映射形：手机号作键，uid 从 JWT 的 sub 兜底
func TestImportKeyedByPhoneUIDFromJWT(t *testing.T) {
	d := t.TempDir()
	tok := "x." + b64url(`{"sub":"jwt-uid-9"}`) + ".y"
	raw := `{"13800000000":{"access_token":"` + tok + `","refresh_token":"RT"}}`
	r := runImport(t, d, raw)
	if r.Added != 1 || r.Items[0].UID != "jwt-uid-9" {
		t.Fatalf("%+v", r.Items)
	}
}

// 8. 闭环：导出文件必须能被网关自己的解析器读回
func TestImportRoundtripParseable(t *testing.T) {
	d := t.TempDir()
	raw := `{"account":{"uid":"u-8","nickname":"闭环"},"auth":{"access_token":"AT8","expires_at":"1795097981"}}`
	runImport(t, d, raw)
	b, err := os.ReadFile(filepath.Join(d, "auths", "workbuddy-u-8.json"))
	if err != nil {
		t.Fatalf("读文件失败: %v", err)
	}
	a, perr := auth.Parse(b)
	if perr != nil {
		t.Fatalf("网关解析失败: %v", perr)
	}
	if a.AccessToken != "AT8" || a.Nickname != "闭环" || a.UID != "u-8" {
		t.Fatalf("字段不对: %+v", a)
	}
	if a.ExpiresAt != 1795097981 {
		t.Fatalf("expiresAt=%d", a.ExpiresAt)
	}
}

// 9. 空输入 / 非 JSON / 数组里没对象 —— 必须报错而不是静默成功
func TestImportBadInput(t *testing.T) {
	d := t.TempDir()
	for _, bad := range []string{"   ", "hello", `"str"`, `[1,2,3]`, `{"foo":"bar"}`} {
		if _, err := ImportJSON(d, bad, false); err == nil {
			t.Fatalf("输入 %q 应报错", bad)
		}
	}
}

// 10. path 穿越防护：uid 带斜杠必须被拒
func TestImportRejectsPathTraversal(t *testing.T) {
	d := t.TempDir()
	r := runImport(t, d, `{"uid":"../../evil","accessToken":"AT"}`)
	if r.Skipped != 1 {
		t.Fatalf("应拒收: %+v", r.Items)
	}
	if _, err := os.Stat(filepath.Join(d, "evil.json")); err == nil {
		t.Fatal("发生了目录穿越")
	}
}

// 11. 更新账号时，载荷里没带的昵称/企业 ID 不能被抹掉
func TestImportUpdateKeepsNickname(t *testing.T) {
	d := t.TempDir()
	first := `{"account":{"uid":"u-11","nickname":"老王","enterpriseId":"e-11"},
	           "auth":{"accessToken":"AT11","expiresAt":1795097981}}`
	if r := runImport(t, d, first); r.Added != 1 {
		t.Fatalf("first added=%d %+v", r.Added, r.Items)
	}
	// 只换 token，不带昵称
	second := `{"uid":"u-11","accessToken":"AT11-new"}`
	if r := runImport(t, d, second); r.Updated != 1 {
		t.Fatalf("second updated=%d %+v", r.Updated, r.Items)
	}
	b, _ := os.ReadFile(filepath.Join(d, "auths", "workbuddy-u-11.json"))
	a, err := auth.Parse(b)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if a.Nickname != "老王" || a.EnterpriseID != "e-11" {
		t.Fatalf("展示字段被抹掉: nick=%q ent=%q", a.Nickname, a.EnterpriseID)
	}
	if a.AccessToken != "AT11-new" {
		t.Fatalf("token 未更新: %q", a.AccessToken)
	}
}

// ---- 自动换票（httptest，绝不打真网络） ----

// 假的换票端点：返回 code=0 + 新 AT/RT
func fakeRefreshServer(t *testing.T, hits *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		if r.URL.Path != "/v2/plugin/auth/token/refresh" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Refresh-Token") == "" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"code":401,"msg":"no refresh token"}`))
			return
		}
		if r.Header.Get("X-Auth-Refresh-Source") != "plugin" {
			t.Errorf("缺 X-Auth-Refresh-Source: plugin")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{
			"accessToken":"NEW-AT","refreshToken":"NEW-RT","expiresIn":5184000,
			"domain":"www.codebuddy.cn"}}`))
	}))
}

// 12. 只给 refreshToken + 开自动换票 → 换到新票并落盘（RT 必须是新的）
func TestImportAutoRefreshSuccess(t *testing.T) {
	var hits int32
	srv := fakeRefreshServer(t, &hits)
	defer srv.Close()
	importChatBaseCN = srv.URL
	t.Cleanup(func() { importChatBaseCN = "" })

	d := t.TempDir()
	raw := `{"account":{"uid":"u-r1","nickname":"换票号"},
	         "auth":{"refresh_token":"OLD-RT","domain":"www.codebuddy.cn","realm":"cn"}}`
	out, err := ImportJSON(d, raw, true)
	if err != nil {
		t.Fatalf("ImportJSON: %v", err)
	}
	var env struct {
		OK   bool         `json:"ok"`
		Data ImportResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("bad envelope: %v", err)
	}
	if env.Data.Added != 1 {
		t.Fatalf("added=%d skipped=%d %+v", env.Data.Added, env.Data.Skipped, env.Data.Items)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("端点被调用 %d 次", hits)
	}

	b, _ := os.ReadFile(filepath.Join(d, "auths", "workbuddy-u-r1.json"))
	a, perr := auth.Parse(b)
	if perr != nil {
		t.Fatalf("网关解析失败: %v", perr)
	}
	if a.AccessToken != "NEW-AT" {
		t.Fatalf("AT 未换成新票: %q", a.AccessToken)
	}
	if a.RefreshToken != "NEW-RT" {
		t.Fatalf("RT 未轮换落盘: %q（旧 RT 已作废，必须写新的）", a.RefreshToken)
	}
	if a.Nickname != "换票号" {
		t.Fatalf("昵称丢了: %q", a.Nickname)
	}
	// expiresIn=60d
	if want := time.Now().Add(60 * 24 * time.Hour).Unix(); a.ExpiresAt < want-3600 || a.ExpiresAt > want+3600 {
		t.Fatalf("expiresAt=%d 与 60 天不符", a.ExpiresAt)
	}
}

// 13. 换票端点 500 → 跳过并给原因，磁盘零文件（不写半截）
func TestImportAutoRefreshUpstreamFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"code":500,"msg":"boom"}`))
	}))
	defer srv.Close()
	importChatBaseCN = srv.URL
	t.Cleanup(func() { importChatBaseCN = "" })

	d := t.TempDir()
	out, _ := ImportJSON(d, `{"13800000000":{"refresh_token":"RT-x"}}`, true)
	var env struct {
		Data ImportResult `json:"data"`
	}
	_ = json.Unmarshal([]byte(out), &env)
	if env.Data.Skipped != 1 || env.Data.Added != 0 {
		t.Fatalf("added=%d skipped=%d", env.Data.Added, env.Data.Skipped)
	}
	if !strings.Contains(env.Data.Items[0].Reason, "换票失败") {
		t.Fatalf("原因里没有换票失败: %+v", env.Data.Items[0])
	}
	ents, _ := os.ReadDir(filepath.Join(d, "auths"))
	if len(ents) != 0 {
		t.Fatalf("换了半截还写了文件: %d 个", len(ents))
	}
}

// 14. 关掉自动换票 → 一条都不能打网络（反证：开关必须真的拦住请求）
func TestImportAutoRefreshDisabledNoNetwork(t *testing.T) {
	var hits int32
	srv := fakeRefreshServer(t, &hits)
	defer srv.Close()
	importChatBaseCN = srv.URL
	t.Cleanup(func() { importChatBaseCN = "" })

	d := t.TempDir()
	out, _ := ImportJSON(d, `{"13800000000":{"refresh_token":"RT-y"}}`, false)
	var env struct {
		Data ImportResult `json:"data"`
	}
	_ = json.Unmarshal([]byte(out), &env)
	if env.Data.Skipped != 1 {
		t.Fatalf("skipped=%d", env.Data.Skipped)
	}
	if !strings.Contains(env.Data.Items[0].Reason, "未开启自动换票") {
		t.Fatalf("原因不对: %+v", env.Data.Items[0])
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("开关关掉了还打了 %d 次网络", n)
	}
}
