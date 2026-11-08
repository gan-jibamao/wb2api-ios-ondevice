# WB 反代 · iOS 自托管版（含 JSON 导入）

把 WorkBuddy / CodeBuddy 账号池变成 OpenAI 兼容 API 的网关，**整个跑在 iPhone 上**：
Go 内核编译成 iOS 静态库（`-buildmode=c-archive`），SwiftUI 外壳，监听 `127.0.0.1:7863`，
同机其它 App（含 Minis）可直接当 OpenAI 端点用。

- 上游源码：<https://github.com/cuicsi666/wb2api-ios>（MIT，Copyright © 2026 Sliverkiss）
- 本仓库：在原版基础上**新增 JSON 导入账号**，并补齐测试与 Release 产物。

## 本仓库相对上游的改动

| 改动 | 文件 | 说明 |
|---|---|---|
| 新增 JSON 导入内核 | `go/wbios/import.go` | 7 种 JSON 形态归一化 → 原子写入 `auths/workbuddy-<uid>.json` |
| 导出 C 接口 | `go/iosmain/main.go` | `WbImportJSON(dataDir, json)` |
| 导入单测 | `go/wbios/import_test.go` | 10 条，含「不归一化必失败」「只给 refreshToken 必跳过」「导出必可被网关解析」三条反证 |
| 导入界面 | `ios/WB2API/ImportSheet.swift` | 粘贴 / 剪贴板 / 选文件（`.fileImporter`），逐条显示新增·更新·跳过原因 |
| 调用与模型 | `ios/WB2API/WBGateway.swift` | `importJSON(_:)`、`WBImportResult` |
| 入口 | `ios/WB2API/ContentView.swift` | 账号页新增「导入 JSON 账号」 |
| CI | `.github/workflows/build.yml` | 增加 `go test ./...`、`nm` 校验导出符号、tag 自动发 Release |

## JSON 导入支持的形态

```jsonc
// 1. 嵌套驼峰（插件 OAuth 原生输出）
{"account":{"uid":"…","nickname":"…"},"auth":{"accessToken":"…","refreshToken":"…","expiresAt":1795097981,"domain":"www.codebuddy.cn","realm":"cn"}}

// 2. 嵌套蛇形
{"account":{"uid":"…"},"auth":{"access_token":"…","refresh_token":"…","expires_at":1795097981}}

// 3. 扁平（驼峰或蛇形）
{"uid":"…","accessToken":"…","refreshToken":"…","expiresAt":1795097981}

// 4. 顶层数组 / 信封
[{"uid":"…","accessToken":"…"}, {"uid":"…","accessToken":"…"}]
{"accounts":[ … ]}   // data / list / items / results / auths 同样识别

// 5. 映射形（手机号或 uid 作键）
{"13800000000":{"access_token":"…","refresh_token":"…"}}
```

归一化规则：

- `expires_at` 毫秒级自动 ÷1000（阈值 1e11，2026 年秒级约 1.8e9）
- `uid` 缺失时先从 `accessToken`/`refreshToken` 的 JWT `payload.sub` 取，再退回映射键（纯数字手机号不算）
- `realm` 缺失按 domain 后缀推断（`.workbuddy.ai` → global，否则 cn）
- **只给 refreshToken 的条目会被跳过并给出原因**，不会写出半个文件（本版不做自动换票）
- 写盘前强制过一次 `auth.Parse` 自检，网关读不出来的文件绝不落盘
- 账号池目录每 5 秒做一次指纹比对（`internal/pool/watch.go`），**导入后无需重启网关**

## 构建

本仓库的 CI 用 GitHub Actions `macos-15` 出未签名 IPA（见 Actions 产物 / Releases）。
本地构建需要 macOS + Xcode + Go 1.23 + XcodeGen：

```bash
cd go
SDK=$(xcrun --sdk iphoneos --show-sdk-path)
CC=$(xcrun --sdk iphoneos -f clang)
GOOS=ios GOARCH=arm64 CGO_ENABLED=1 CC="$CC" \
  CGO_CFLAGS="-isysroot $SDK -miphoneos-version-min=15.0 -arch arm64" \
  CGO_LDFLAGS="-isysroot $SDK -miphoneos-version-min=15.0 -arch arm64" \
  go build -tags carchive -buildmode=c-archive -ldflags "-s -w" -o ../ios/GoLib/libwb.a ./iosmain

cd ../ios && brew install xcodegen && xcodegen generate
xcodebuild -project WB2API.xcodeproj -scheme WB2API -configuration Release \
  -sdk iphoneos -derivedDataPath build \
  CODE_SIGNING_ALLOWED=NO CODE_SIGNING_REQUIRED=NO ONLY_ACTIVE_ARCH=NO build
```

产物为未签名 IPA，用全能签 / SideStore / AltStore / TrollStore 重签后安装。

## 使用

1. 重签安装，打开 App，点「启动反代」
2. 「导入 JSON 账号」→ 粘贴或选文件 → 开始导入
3. 账号页出现条目、状态为「可用」即入池（5 秒内自动加载）
4. 客户端配置：`http://127.0.0.1:7863/v1`，`Authorization: Bearer <App 内显示的 API Key>`

数据目录：`文件 App → 我的 iPhone → WB反代 → WB2API`（`config.json` / `auths/` / `data/` / `gateway.log`）。

## 许可

MIT，遵循上游。原版版权归 Sliverkiss，本仓库改动同样以 MIT 发布。
