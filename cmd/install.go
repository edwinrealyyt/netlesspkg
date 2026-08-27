package cmd

import (
	"flag"
	"fmt"
	"netlesspkg/pkg/archive"
	"netlesspkg/pkg/core"
	"os"
	"runtime"
	"strings"
)

func runInstall(args []string) {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	inFile := fs.String("i", "packages.bundle", "输入 bundle 路径")
	packagesStr := fs.String("p", "", "逗号分隔的包名列表（必填）")
	
	if err := fs.Parse(args); err != nil {
		fmt.Printf("参数解析错误: %v\n", err)
		os.Exit(1)
	}

	if *packagesStr == "" {
		fmt.Println("必须指定包名列表 (-p)")
		fs.Usage()
		os.Exit(1)
	}
	targetPackages := strings.Split(*packagesStr, ",")

	if runtime.GOOS != "windows" && os.Getuid() != 0 {
		fmt.Println("警告: 安装操作可能需要 root 权限，请确保拥有足够权限！")
	}

	pm, err := core.DetectManager()
	if err != nil {
		fmt.Printf("检测包管理器失败: %v\n", err)
		os.Exit(1)
	}

	tmpDir, err := os.MkdirTemp("", "netlesspkg-install-*")
	if err != nil {
		fmt.Printf("创建临时目录失败: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	if err := archive.ExtractBundle(*inFile, tmpDir); err != nil {
		fmt.Printf("解压 bundle 失败: %v\n", err)
		os.Exit(1)
	}

	if err := pm.InjectPackagesAndInstall(tmpDir, targetPackages); err != nil {
		fmt.Printf("安装失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("安装成功！")
}
