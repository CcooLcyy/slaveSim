package main

// 发布工作流在构建时写入版本、提交与构建时间；本地构建保持开发标记。
var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)
