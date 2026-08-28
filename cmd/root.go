package cmd

import (
	"fmt"
	"os"
	"strings"
)

// stringSliceFlag 支持多次传入同一命令行标志
type stringSliceFlag []string

func (s *stringSliceFlag) String() string {
	return strings.Join(*s, ", ")
}

func (s *stringSliceFlag) Set(val string) error {
	*s = append(*s, val)
	return nil
}

func Execute() {
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(1)
	}

	// 从参数列表中找到第一个非标志参数作为子命令
	// 支持 "netlesspkg install -i xxx" 和 "netlesspkg -i xxx install" 两种写法
	command, args := findCommand(os.Args[1:])

	if command == "" {
		fmt.Println("错误: 未指定子命令")
		printHelp()
		os.Exit(1)
	}

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
	case "help", "-h", "--help":
		printHelp()
	case "version", "-v", "--version":
		fmt.Println("NetlessPkg v0.1.0")
	default:
		fmt.Printf("未知命令: %s\n", command)
		fmt.Println("提示: 请将子命令放在参数前面，例如:")
		fmt.Println("  netlesspkg install -i packages.bundle -p nginx")
		fmt.Println()
		printHelp()
		os.Exit(1)
	}
}

// findCommand 从参数列表中找到第一个非标志参数作为子命令名。
// 剩余的标志和参数都作为子命令的 args 返回。
// 例如：["-i", "foo.json", "plan", "-p", "nginx"]
//
//	返回 command="plan", args=["-i", "foo.json", "-p", "nginx"]
func findCommand(rawArgs []string) (command string, args []string) {
	validCommands := map[string]bool{
		"export": true, "sync-meta": true, "plan": true,
		"fetch": true, "install": true, "verify": true,
		"help": true, "version": true,
		"-h": true, "--help": true, "-v": true, "--version": true,
	}

	cmdIndex := -1
	for i, arg := range rawArgs {
		if validCommands[arg] {
			cmdIndex = i
			command = arg
			break
		}
	}

	if cmdIndex == -1 {
		// 没有找到已知子命令，把第一个非标志参数当作命令（会在 switch 中触发"未知命令"）
		for i, arg := range rawArgs {
			if !strings.HasPrefix(arg, "-") {
				return arg, append(rawArgs[:i], rawArgs[i+1:]...)
			}
		}
		return "", rawArgs
	}

	// 将子命令从参数列表中移除，其余的都传给子命令
	args = make([]string, 0, len(rawArgs)-1)
	args = append(args, rawArgs[:cmdIndex]...)
	args = append(args, rawArgs[cmdIndex+1:]...)
	return command, args
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
  [内网] netlesspkg install -i packages.bundle -p nginx,docker-ce

高级参数 (sync-meta / fetch 可用):
  --replace, -r <old>=<new>   外网下载时重写 URL (如 --replace mirrors.cloud.aliyuncs.com=mirrors.aliyun.com)
  --no-auto-replace           禁用常见云厂商内网源(阿里云/腾讯云/华为云)的自动公网映射`)
}
