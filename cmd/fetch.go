package cmd

import (
	"encoding/json"
	"flag"
	"fmt"
	"netlesspkg/pkg/archive"
	"netlesspkg/pkg/core"
	"netlesspkg/pkg/downloader"
	"os"
	"path/filepath"
)

func runFetch(args []string) {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	inFile := fs.String("i", "download_plan.json", "输入文件路径")
	outFile := fs.String("o", "packages.bundle", "输出 bundle 路径")
	jobs := fs.Int("j", 4, "并发数")
	noAutoReplace := fs.Bool("no-auto-replace", false, "禁用内置云厂商内网源自动映射为公网源")

	var replaceRules stringSliceFlag
	fs.Var(&replaceRules, "replace", "URL 替换规则，格式为 <旧地址>=<新地址>，支持指定多次 (例如 --replace mirrors.cloud.aliyuncs.com=mirrors.aliyun.com)")
	fs.Var(&replaceRules, "r", "URL 替换规则 (简写)")

	if err := fs.Parse(args); err != nil {
		fmt.Printf("参数解析错误: %v\n", err)
		os.Exit(1)
	}

	data, err := os.ReadFile(*inFile)
	if err != nil {
		fmt.Printf("读取计划文件失败: %v\n", err)
		os.Exit(1)
	}

	var plan core.DownloadPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		fmt.Printf("解析 JSON 失败: %v\n", err)
		os.Exit(1)
	}

	rewriter, err := core.NewURLRewriter(replaceRules, !*noAutoReplace)
	if err != nil {
		fmt.Printf("初始化 URL 重写规则失败: %v\n", err)
		os.Exit(1)
	}

	tmpDir, err := os.MkdirTemp("", "netlesspkg-fetch-*")
	if err != nil {
		fmt.Printf("创建临时目录失败: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	var tasks []downloader.DownloadTask
	replacedCount := 0

	for _, p := range plan.Packages {
		downloadURL, replaced, reason := rewriter.RewriteURL(p.URL)
		if replaced {
			replacedCount++
			if replacedCount <= 3 {
				fmt.Printf("[URL 重写] %s (%s)\n", downloadURL, reason)
			}
		}
		tasks = append(tasks, downloader.DownloadTask{
			URL:      downloadURL,
			SavePath: filepath.Join(tmpDir, p.Filename),
			SHA256:   p.SHA256,
			Size:     p.Size,
		})
	}

	if replacedCount > 3 {
		fmt.Printf("[URL 重写] 共计重写了 %d 个安装包 URL 为公网镜像源\n", replacedCount)
	}

	results := downloader.Download(tasks, downloader.Options{
		Concurrency:  *jobs,
		RetryCount:   3,
		EnableResume: true,
		ShowProgress: true,
	})

	failCount := 0
	for _, r := range results {
		if r.Error != nil {
			fmt.Printf("\n❌ 下载安装包失败: %s\n   错误信息: %v\n", r.Task.URL, r.Error)
			if hint := core.CheckInternalURLError(r.Task.URL); hint != "" {
				fmt.Printf("   %s\n", hint)
			}
			failCount++
		}
	}
	if failCount > 0 {
		fmt.Printf("\n共有 %d 个包下载失败，请检查网络或配置 --replace 参数。\n", failCount)
		os.Exit(1)
	}

	if err := archive.CreateBundle(*outFile, tmpDir); err != nil {
		fmt.Printf("创建 bundle 失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ 成功获取 %d 个包并打包至 %s\n", len(tasks), *outFile)
}
