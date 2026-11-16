// Package webui 内嵌 Web UI 静态资源（单二进制交付，无外部依赖）。
package webui

import (
	"embed"
	"io/fs"
)

//go:embed static
var static embed.FS

// StaticFS 返回根位于 static/ 的子文件系统，供 http.FileServer 使用。
func StaticFS() fs.FS {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err) // embed 静态内容，不可能失败
	}
	return sub
}

// IndexHTML 返回首页 HTML。
func IndexHTML() []byte {
	b, err := static.ReadFile("static/index.html")
	if err != nil {
		panic(err)
	}
	return b
}
