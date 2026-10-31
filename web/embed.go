//go:build embedweb

// Package web 提供发布构建内嵌的 Admin 和用户侧前端。
package web

import (
	"embed"
	"io/fs"
)

//go:embed admin/dist portal/dist
var assets embed.FS

// Assets 返回两个应用的构建产物。make build 和 Docker 构建会先构建前端。
func Assets() (admin, portal fs.FS) {
	admin, _ = fs.Sub(assets, "admin/dist")
	portal, _ = fs.Sub(assets, "portal/dist")
	return
}
