package wbios

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"workbuddy2api/internal/auth"
)

// ImportItem 单条导入结果。
type ImportItem struct {
	UID      string `json:"uid"`
	Nickname string `json:"nickname"`
	Status   string `json:"status"` // added / updated / skipped
	Reason   string `json:"reason,omitempty"`
	File     string `json:"file,omitempty"`
}

// ImportResult 导入汇总。
type ImportResult struct {
	Total   int          `json:"total"`
	Added   int          `json:"added"`
	Updated int          `json:"updated"`
	Skipped int          `json:"skipped"`
	Items   []ImportItem `json:"items"`
}

// normAccount 归一化后的账号（内部用）。
type normAccount struct {
	UID          string
	Nickname     string
	EnterpriseID string
	AccessToken  string
	RefreshToken string
	DeviceToken  string
	ExpiresAt    int64
	Domain       string
	Realm        string
	SourceKey    string
}

// record 一条候选记录：原始 map + 来源键（映射形的手机号/uid）。
type record struct {
	mapData map[string]any
	key     string
}

// ImportJSON 导入账号 JSON，返回 {"ok":true,"data":ImportResult}。
//
// 支持形态：
//
//	嵌套驼峰  {"account":{uid,nickname},"auth":{accessToken,refreshToken,expiresAt,domain,realm}}
//	嵌套蛇形  {"account":{…},"auth":{access_token,refresh_token,expires_at}}
//	扁平驼峰  {uid, accessToken, refreshToken, expiresAt, domain, realm}
//	扁平蛇形  {uid, access_token, refresh_token, expires_at, domain, realm}
//	顶层数组  [ <以上任意形态>, … ]
//	信封      {"accounts":[…]} / {"data":[…]} / {"list":[…]} / {"items":[…]} / {"results":[…]} / {"auths":[…]}
//	映射形    {"<手机号或 uid>": {…}, …}
//
// 全部归一化为网关磁盘形态（嵌套驼峰）后原子写入 auths/workbuddy-<uid>.json。
// 写盘前强制过一次 auth.Parse 自检，绝不写出网关读不出来的文件。
//
// allowRefresh=true 时，只有 refreshToken 的条目会先联网换票（upstream.RefreshToken），
// 换到的新 AT/RT 立刻落盘——RT 是单次轮换，晚一步原账号就作废。
func ImportJSON(dataDir, raw string, allowRefresh bool) (string, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return "", fmt.Errorf("dataDir 不能为空")
	}
	dir := filepath.Join(dataDir, "auths")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("创建凭证目录失败：%w", err)
	}

	recs, err := flattenRecords([]byte(raw))
	if err != nil {
		return "", err
	}

	res := ImportResult{Items: []ImportItem{}}
	seen := map[string]int{} // uid → res.Items 下标，同批次后出现的覆盖前面

	for _, rec := range recs {
		res.Total++

		na, nerr := normalizeRecord(rec.mapData, rec.key)

		// 只有 refreshToken：开了自动换票就换，换到新票后按普通账号继续落盘。
		if errors.Is(nerr, errNeedRefresh) {
			if !allowRefresh {
				nerr = fmt.Errorf("只有 refreshToken，缺 accessToken（未开启自动换票）")
			} else {
				na, nerr = doRefresh(na)
			}
		}

		if nerr != nil {
			res.Skipped++
			uid := na.UID
			if uid == "" {
				uid = rec.key
			}
			res.Items = append(res.Items, ImportItem{UID: uid, Status: "skipped", Reason: nerr.Error()})
			continue
		}

		name := "workbuddy-" + sanitizeUID(na.UID) + ".json"
		out := filepath.Join(dir, name)
		_, statErr := os.Stat(out)
		existed := statErr == nil

		// 更新已有账号时，新载荷里没带的展示字段不能把旧的抹掉
		// （典型：只拿新 token 重导一次，昵称/企业 ID 不该消失）。
		if existed {
			if old, perr := loadAuthFile(out); perr == nil {
				if na.Nickname == "" {
					na.Nickname = old.Nickname
				}
				if na.EnterpriseID == "" {
					na.EnterpriseID = old.EnterpriseID
				}
				if na.DeviceToken == "" {
					na.DeviceToken = old.DeviceToken
				}
				if na.Realm == "" {
					na.Realm = old.Realm()
				}
			}
		}

		doc := map[string]any{
			"account": map[string]any{
				"uid":          na.UID,
				"enterpriseId": na.EnterpriseID,
				"nickname":     na.Nickname,
			},
			"auth": map[string]any{
				"accessToken":  na.AccessToken,
				"refreshToken": na.RefreshToken,
				"expiresAt":    na.ExpiresAt,
				"domain":       na.Domain,
				"realm":        na.Realm,
			},
		}
		// 与 auth.SaveAtomic 同策略：device_token 非空才写，避免给旧文件引入空键。
		if na.DeviceToken != "" {
			doc["device_token"] = na.DeviceToken
		}

		b, merr := json.MarshalIndent(doc, "", " ")
		if merr != nil {
			res.Skipped++
			res.Items = append(res.Items, ImportItem{UID: na.UID, Status: "skipped", Reason: "序列化失败"})
			continue
		}
		if _, perr := auth.Parse(b); perr != nil {
			res.Skipped++
			res.Items = append(res.Items, ImportItem{
				UID:    na.UID,
				Status: "skipped",
				Reason: "自检未通过：" + perr.Error(),
			})
			continue
		}

		if werr := writeFileAtomic(out, b); werr != nil {
			res.Skipped++
			res.Items = append(res.Items, ImportItem{UID: na.UID, Status: "skipped", Reason: werr.Error()})
			continue
		}

		item := ImportItem{UID: na.UID, Nickname: na.Nickname, File: name}
		if existed {
			item.Status = "updated"
			res.Updated++
			if i, ok := seen[na.UID]; ok {
				res.Items[i] = item // 同批次重复 uid：只保留最后一条，不重复计数
				continue
			}
		} else {
			item.Status = "added"
			res.Added++
		}
		seen[na.UID] = len(res.Items)
		res.Items = append(res.Items, item)
	}

	b, _ := json.Marshal(map[string]any{"ok": true, "data": res})
	return string(b), nil
}

// loadAuthFile 读一个已有凭证文件（更新时用于保留展示字段）。
func loadAuthFile(path string) (*auth.Auth, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return auth.Parse(b)
}

// writeFileAtomic 写临时文件再 rename，权限 0600（与 login.go 落盘一致）。
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("写入失败：%w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换失败：%w", err)
	}
	return nil
}

// flattenRecords 把任意形状的 JSON 展平成候选记录列表。
func flattenRecords(raw []byte) ([]record, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("内容为空")
	}

	switch trimmed[0] {
	case '[':
		var arr []any
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return nil, fmt.Errorf("JSON 数组解析失败：%w", err)
		}
		out := make([]record, 0, len(arr))
		for _, v := range arr {
			if m, ok := v.(map[string]any); ok {
				out = append(out, record{mapData: m})
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("数组里没有账号对象")
		}
		return out, nil

	case '{':
		var obj map[string]any
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return nil, fmt.Errorf("JSON 解析失败：%w", err)
		}
		if looksLikeAccount(obj) {
			return []record{{mapData: obj}}, nil
		}
		// 信封：值为数组或单对象
		for _, k := range []string{"accounts", "data", "list", "items", "results", "auths"} {
			v, ok := obj[k]
			if !ok {
				continue
			}
			if arr, isArr := v.([]any); isArr {
				out := make([]record, 0, len(arr))
				for _, x := range arr {
					if m, isMap := x.(map[string]any); isMap {
						out = append(out, record{mapData: m})
					}
				}
				if len(out) > 0 {
					return out, nil
				}
			}
			if m, isMap := v.(map[string]any); isMap {
				if looksLikeAccount(m) {
					return []record{{mapData: m}}, nil
				}
			}
		}
		// 映射形：键 → 对象（手机号 / uid 作键）
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]record, 0, len(keys))
		for _, k := range keys {
			if m, ok := obj[k].(map[string]any); ok {
				out = append(out, record{mapData: m, key: k})
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("没有找到账号对象")
		}
		return out, nil

	default:
		return nil, fmt.Errorf("不是 JSON 对象或数组")
	}
}

// looksLikeAccount 判断 map 自身是否就是一条账号记录。
func looksLikeAccount(m map[string]any) bool {
	if hasAny(m, "accessToken", "access_token", "refreshToken", "refresh_token") {
		return true
	}
	for _, k := range []string{"auth", "account", "credentials", "token"} {
		if sub, ok := m[k].(map[string]any); ok {
			if hasAny(sub, "accessToken", "access_token", "refreshToken", "refresh_token") {
				return true
			}
		}
	}
	return false
}

// normalizeRecord 把一条候选记录归一化成可落盘账号。
func normalizeRecord(m map[string]any, sourceKey string) (normAccount, error) {
	var na normAccount
	na.SourceKey = strings.TrimSpace(sourceKey)

	// 分层合并：顶层 + account/auth/credentials/user/token 子对象。
	layers := []map[string]any{m}
	for _, k := range []string{"auth", "account", "credentials", "user", "token"} {
		if sub, ok := m[k].(map[string]any); ok {
			layers = append(layers, sub)
		}
	}

	na.AccessToken = firstString(layers, "accessToken", "access_token", "access-token", "token", "at")
	na.RefreshToken = firstString(layers, "refreshToken", "refresh_token", "refresh-token", "rt")
	na.DeviceToken = firstString(layers, "device_token", "deviceToken")
	na.UID = firstString(layers, "uid", "user_id", "userId", "userid", "sub", "id")
	na.Nickname = firstString(layers, "nickname", "nickName", "nick", "name", "displayName", "display_name")
	na.EnterpriseID = firstString(layers, "enterpriseId", "enterprise_id", "entId")
	na.Domain = firstString(layers, "domain", "host")
	na.Realm = firstString(layers, "realm", "region")

	na.ExpiresAt = firstInt(layers, "expiresAt", "expires_at", "expireAt", "expires", "expiry", "exp")

	// 单位归一：毫秒 → 秒。2026 年秒级约 1.8e9、毫秒级约 1.8e12，1e11 是安全分界。
	if na.ExpiresAt > 100_000_000_000 {
		na.ExpiresAt /= 1000
	}

	// accessToken 是硬前提，先判：它缺失时「uid 推不出来」只是表象，
	// 真正卡住的是没票。只给 refreshToken 的条目不是错误而是「可换票」，
	// 交给调用方决定要不要联网换（errNeedRefresh 哨兵）。
	if strings.TrimSpace(na.AccessToken) == "" {
		if strings.TrimSpace(na.RefreshToken) != "" {
			return na, errNeedRefresh
		}
		return na, fmt.Errorf("缺 accessToken")
	}
	return finalize(na)
}

// errNeedRefresh 表示「只有 refreshToken、缺 accessToken」，开自动换票即可救回。
var errNeedRefresh = errors.New("需要换票")

// finalize 补齐 uid / realm / domain，并做路径安全校验。
// 归一化尾部与「换票之后」共用同一套收尾逻辑，避免两条路径判据漂移。
func finalize(na normAccount) (normAccount, error) {
	// uid 兜底 1：AT / RT 的 JWT payload.sub
	if na.UID == "" {
		if s := jwtSub(na.AccessToken); s != "" {
			na.UID = s
		} else if s := jwtSub(na.RefreshToken); s != "" {
			na.UID = s
		}
	}
	// uid 兜底 2：映射形的键（但手机号不是 uid）
	if na.UID == "" && looksLikeUID(na.SourceKey) {
		na.UID = na.SourceKey
	}
	if na.UID == "" {
		return na, fmt.Errorf("缺 uid（且无法从 token 的 sub 推出）")
	}

	na.Realm = auth.ResolveRealm(na.Realm, na.Domain)
	if na.Domain == "" {
		if na.Realm == "global" {
			na.Domain = "www.workbuddy.ai"
		} else {
			na.Domain = "www.codebuddy.cn"
		}
	}

	if strings.ContainsAny(na.UID, `/\`) || na.UID == "." || na.UID == ".." {
		return na, fmt.Errorf("uid 含非法字符：%q", na.UID)
	}
	return na, nil
}

// doRefresh 用 refreshToken 换票并回填账号字段。
//
// 注意 RT 是单次轮换：换票成功后旧 RT 立刻作废，新 RT 必须马上落盘，
// 否则这个账号就永久废了。落盘由调用方紧接着完成（ImportJSON 里）。
func doRefresh(na normAccount) (normAccount, error) {
	refreshed, err := refreshAccount(na.UID, na.RefreshToken, na.Domain, na.Realm, na.EnterpriseID)
	if err != nil {
		return na, fmt.Errorf("换票失败：%w", err)
	}
	na.AccessToken = refreshed.AccessToken
	na.RefreshToken = refreshed.RefreshToken // 轮换后的新 RT，不是原来那个
	na.ExpiresAt = refreshed.ExpiresAt
	if na.Domain == "" {
		na.Domain = refreshed.Domain
	}
	if na.UID == "" {
		na.UID = refreshed.UID
	}
	return finalize(na)
}

// sanitizeUID 文件名安全化：只留字母数字与 ._-
func sanitizeUID(uid string) string {
	var b strings.Builder
	for _, r := range uid {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()
	if s == "" || s == "." || s == ".." {
		return "imported"
	}
	return s
}

// jwtSub 不验签解出 JWT payload 的 sub（只用于 uid 兜底，不参与任何鉴权）。
func jwtSub(tok string) string {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return ""
	}
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return ""
	}
	seg := strings.TrimRight(parts[1], "=")
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return ""
	}
	var p struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return ""
	}
	return strings.TrimSpace(p.Sub)
}

// looksLikeUID 手机号（纯数字）不算 uid，UUID 这类才算。
func looksLikeUID(s string) bool {
	if len(s) < 8 {
		return false
	}
	digits := 0
	total := 0
	for _, r := range s {
		total++
		if r >= '0' && r <= '9' {
			digits++
		}
	}
	return digits != total
}

func hasAny(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

func firstString(layers []map[string]any, keys ...string) string {
	for _, m := range layers {
		for _, k := range keys {
			v, ok := m[k]
			if !ok {
				continue
			}
			switch t := v.(type) {
			case string:
				if s := strings.TrimSpace(t); s != "" {
					return s
				}
			case json.Number:
				return t.String()
			case float64:
				return fmt.Sprintf("%.0f", t)
			}
		}
	}
	return ""
}

func firstInt(layers []map[string]any, keys ...string) int64 {
	for _, m := range layers {
		for _, k := range keys {
			v, ok := m[k]
			if !ok {
				continue
			}
			switch t := v.(type) {
			case float64:
				return int64(t)
			case json.Number:
				if n, err := t.Int64(); err == nil {
					return n
				}
			case string:
				var n int64
				if _, err := fmt.Sscanf(strings.TrimSpace(t), "%d", &n); err == nil {
					return n
				}
			}
		}
	}
	return 0
}
