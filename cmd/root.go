package cmd

import (
	"fmt"
	"os"
)

func Execute() {
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(1)
	}

	command := os.Args[1]
	args := os.Args[2:]

	switch command {
	case "export":
		runExport(args)
	case "sync-meta":
		runSyncMeta(args)
	case "plan":
		runPlan(args)
	case "fetch":
		runFetch(args)
	case "install":
		runInstall(args)
	case "verify":
		runVerify(args)
	case "help":
		printHelp()
	case "version":
		fmt.Println("NetlessPkg v0.1.0")
	default:
		fmt.Printf("未知命令: %s\n", command)
		printHelp()
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Println(`NetlessPkg - 离线环境通用包管理工具 v0.1.0

用法: netlesspkg <命令> [参数]

可用命令:
  export      扫描系统源配置，导出元数据下载清单 (meta_request.json)
  sync-meta   根据清单下载元数据，生成 metadata.bundle
  plan        注入元数据并计算依赖，生成下载计划 (download_plan.json)
  fetch       根据下载计划获取安装包，生成 packages.bundle
  install     解压安装包并执行离线安装
  verify      校验 bundle 中文件的完整性
  help        显示帮助信息
  version     显示版本信息

典型工作流:
  [内网] netlesspkg export -o meta_request.json
  [外网] netlesspkg sync-meta -i meta_request.json -o metadata.bundle
  [内网] netlesspkg plan -i metadata.bundle -p nginx,docker-ce -o download_plan.json
  [外网] netlesspkg fetch -i download_plan.json -o packages.bundle
  [内网] netlesspkg install -i packages.bundle -p nginx,docker-ce`)
}
