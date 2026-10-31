// Package api 内嵌 Patato 的 API 契约（openapi.yaml）。
//
// 契约是前后端分离的单一事实来源：服务端路由表与它保持一致（由测试检查），
// 前端的 TypeScript 类型、skill 工具清单都从它生成。
package api

import _ "embed"

//go:embed openapi.yaml
var Spec []byte
