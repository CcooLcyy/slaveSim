package main

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var baseVersion string

// 发布工作流在构建时写入版本、提交与构建时间；本地构建保持开发标记。
var (
	version   = ""
	commit    = "unknown"
	buildTime = "unknown"
)

func init() {
	if version == "" {
		version = strings.TrimSpace(baseVersion) + "-dev"
	}
}
