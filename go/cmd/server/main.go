// cmd/server workbuddy2api 命令行入口（薄壳）：装配逻辑在 internal/app，
// 与 iOS App（wbios 包）共用同一份装配代码。
package main

import "workbuddy2api/internal/app"

func main() {
	app.Run()
}
