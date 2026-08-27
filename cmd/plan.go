package cmd

import (
	"encoding/json"
	"flag"
	"fmt"
	"netlesspkg/pkg/archive"
	"netlesspkg/pkg/core"
	"os"
	"strings"
)

func runPlan(args []string) {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	inFile := fs.String("i", "metadata.bundle", "输入 bundle 路径")
	packagesStr := fs.String("p", "", "逗号分隔的包名列表（必填）")
	outFile := fs.String("o", "download_plan.json", "输出文件路径")
	
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

	pm, err := core.DetectManager()
	if err != nil {
		fmt.Printf("检测包管理器失败: %v\n", err)
		os.Exit(1)
	}

	tmpDir, err := os.MkdirTemp("", "netlesspkg-plan-*")
	if err != nil {
		fmt.Printf("创建临时目录失败: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	if err := archive.ExtractBundle(*inFile, tmpDir); err != nil {
		fmt.Printf("解压 bundle 失败: %v\n", err)
		os.Exit(1)
	}

	pkgs, err := pm.InjectMetaAndPlan(tmpDir, targetPackages)
	if err != nil {
		fmt.Printf("计算下载计划失败: %v\n", err)
		os.Exit(1)
	}

	plan := core.DownloadPlan{
		TargetPackages: targetPackages,
		PackageManager: pm.Name(),
		Packages:       pkgs,
	}

	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		fmt.Printf("JSON 序列化失败: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(*outFile, data, 0644); err != nil {
		fmt.Printf("写入计划文件失败: %v\n", err)
		os.Exit(1)
	}

	var totalSize int64
	for _, p := range pkgs {
		totalSize += p.Size
	}

	fmt.Printf("成功生成下载计划: %d 个包, 总大小约 %d 字节, 保存至 %s\n", len(pkgs), totalSize, *outFile)
}
