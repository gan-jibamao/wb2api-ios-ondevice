package wbios

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
)

// runImport 跑一次导入并解出 ImportResult。
func runImport(t *testing.T, dir, raw string) ImportResult {
	t.Helper()
	out, err := ImportJSON(dir, raw)
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
		if _, err := ImportJSON(d, bad); err == nil {
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
