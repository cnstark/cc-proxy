// Package web 内嵌后台前端静态资源（原生 HTML/CSS/JS + vendored Chart.js）。
// 由 internal/admin 导入并通过 //go:embed 打包进 ccp-proxy 二进制，零构建链。
package web

import "embed"

//go:embed index.html app.js style.css chart.min.js
var FS embed.FS
