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

func runSyncMeta(args []string) {
	fs := flag.NewFlagSet("sync-meta", flag.ContinueOnError)
	inFile := fs.String("i", "meta_request.json", "输入文件路径")
	outFile := fs.String("o", "metadata.bundle", "输出 bundle 路径")

	if err := fs.Parse(args); err != nil {
		fmt.Printf("参数解析错误: %v\n", err)
		os.Exit(1)
	}

	data, err := os.ReadFile(*inFile)
	if err != nil {
		fmt.Printf("读取请求文件失败: %v\n", err)
		os.Exit(1)
	}

	var req core.MetaRequest
	if err := json.Unmarshal(data, &req); err != nil {
		fmt.Printf("解析 JSON 失败: %v\n", err)
		os.Exit(1)
	}

	tmpDir, err := os.MkdirTemp("", "netlesspkg-syncmeta-*")
	if err != nil {
		fmt.Printf("创建临时目录失败: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	var tasks []downloader.DownloadTask
	for _, f := range req.Files {
		tasks = append(tasks, downloader.DownloadTask{
			URL:      f.URL,
			SavePath: filepath.Join(tmpDir, f.SavePath),
		})
	}

	results := downloader.Download(tasks, downloader.Options{
		Concurrency:  4,
		RetryCount:   3,
		EnableResume: true,
		ShowProgress: true,
	})

	for _, r := range results {
		if r.Error != nil {
			fmt.Printf("下载元数据失败: %s - %v\n", r.Task.URL, r.Error)
			os.Exit(1)
		}
	}

	if err := archive.CreateBundle(*outFile, tmpDir); err != nil {
		fmt.Printf("创建 bundle 失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("成功同步 %d 个元数据文件到 %s\n", len(req.Files), *outFile)
}
