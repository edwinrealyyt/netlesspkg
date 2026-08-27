package cmd

import (
	"encoding/json"
	"flag"
	"fmt"
	"netlesspkg/pkg/core"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func runExport(args []string) {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	outFile := fs.String("o", "meta_request.json", "输出文件路径")

	if err := fs.Parse(args); err != nil {
		fmt.Printf("参数解析错误: %v\n", err)
		os.Exit(1)
	}

	pm, err := core.DetectManager()
	if err != nil {
		fmt.Printf("检测包管理器失败: %v\n", err)
		os.Exit(1)
	}

	urls, err := pm.GetMetadataURLs()
	if err != nil {
		fmt.Printf("获取元数据 URL 失败: %v\n", err)
		os.Exit(1)
	}

	// 检测系统架构
	arch := detectArch(pm.Name())

	// 确定 OS 族
	osFamily := "unknown"
	switch pm.Name() {
	case "apt":
		osFamily = "debian"
	case "yum":
		osFamily = "rhel"
	}

	req := core.MetaRequest{
		OSFamily: osFamily,
		Arch:     arch,
		Files:    urls,
	}

	data, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		fmt.Printf("JSON 序列化失败: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(*outFile, data, 0644); err != nil {
		fmt.Printf("写入文件失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("系统: %s/%s, 包管理器: %s\n", osFamily, arch, pm.Name())
	fmt.Printf("成功导出 %d 个元数据文件到 %s\n", len(urls), *outFile)
}

// detectArch 检测系统架构
func detectArch(pmName string) string {
	if pmName == "apt" {
		out, err := exec.Command("dpkg", "--print-architecture").Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	}
	if pmName == "yum" {
		out, err := exec.Command("uname", "-m").Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	}
	return runtime.GOARCH
}
