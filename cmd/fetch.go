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

	tmpDir, err := os.MkdirTemp("", "netlesspkg-fetch-*")
	if err != nil {
		fmt.Printf("创建临时目录失败: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	var tasks []downloader.DownloadTask
	for _, p := range plan.Packages {
		tasks = append(tasks, downloader.DownloadTask{
			URL:      p.URL,
			SavePath: filepath.Join(tmpDir, p.Filename),
			SHA256:   p.SHA256,
			Size:     p.Size,
		})
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
			fmt.Printf("下载失败: %s - %v\n", r.Task.URL, r.Error)
			failCount++
		}
	}
	if failCount > 0 {
		fmt.Printf("共有 %d 个包下载失败\n", failCount)
		os.Exit(1)
	}

	if err := archive.CreateBundle(*outFile, tmpDir); err != nil {
		fmt.Printf("创建 bundle 失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("成功获取 %d 个包并打包至 %s\n", len(tasks), *outFile)
}
