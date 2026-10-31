//go:build !embedweb

package web

import "io/fs"

// Assets 在普通 Go 开发构建中关闭静态托管，前端由 Vite 提供。
// 使用 make build 或 -tags embedweb 构建包含前端的服务端。
func Assets() (admin, portal fs.FS) { return nil, nil }
