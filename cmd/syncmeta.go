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
	"strings"
)

type stringSliceFlag []string

func (s *stringSliceFlag) String() string {
	return strings.Join(*s, ", ")
}

func (s *stringSliceFlag) Set(val string) error {
	*s = append(*s, val)
	return nil
}

func runSyncMeta(args []string) {
	fs := flag.NewFlagSet("sync-meta", flag.ContinueOnError)
	inFile := fs.String("i", "meta_request.json", "输入文件路径")
	outFile := fs.String("o", "metadata.bundle", "输出 bundle 路径")
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
		fmt.Printf("读取请求文件失败: %v\n", err)
		os.Exit(1)
	}

	var req core.MetaRequest
	if err := json.Unmarshal(data, &req); err != nil {
		fmt.Printf("解析 JSON 失败: %v\n", err)
		os.Exit(1)
	}

	rewriter, err := core.NewURLRewriter(replaceRules, !*noAutoReplace)
	if err != nil {
		fmt.Printf("初始化 URL 重写规则失败: %v\n", err)
		os.Exit(1)
	}

	tmpDir, err := os.MkdirTemp("", "netlesspkg-syncmeta-*")
	if err != nil {
		fmt.Printf("创建临时目录失败: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	var tasks []downloader.DownloadTask
	replacedCount := 0

	for _, f := range req.Files {
		downloadURL, replaced, reason := rewriter.RewriteURL(f.URL)
		if replaced {
			replacedCount++
			if replacedCount <= 3 {
				fmt.Printf("[URL 重写] %s (%s)\n", downloadURL, reason)
			}
		}
		tasks = append(tasks, downloader.DownloadTask{
			URL:      downloadURL,
			SavePath: filepath.Join(tmpDir, f.SavePath), // 严格保持原始 SavePath 不变，确保内网注入匹配
		})
	}

	if replacedCount > 3 {
		fmt.Printf("[URL 重写] 共计重写了 %d 个内网源 URL 为公网镜像源\n", replacedCount)
	}

	results := downloader.Download(tasks, downloader.Options{
		Concurrency:  4,
		RetryCount:   3,
		EnableResume: true,
		ShowProgress: true,
	})

	failCount := 0
	for _, r := range results {
		if r.Error != nil {
			fmt.Printf("\n❌ 下载元数据失败: %s\n   错误信息: %v\n", r.Task.URL, r.Error)
			if hint := core.CheckInternalURLError(r.Task.URL); hint != "" {
				fmt.Printf("   %s\n", hint)
			}
			failCount++
		}
	}

	if failCount > 0 {
		fmt.Printf("\n共有 %d 个元数据文件下载失败，请检查网络或配置 --replace 参数。\n", failCount)
		os.Exit(1)
	}

	if err := archive.CreateBundle(*outFile, tmpDir); err != nil {
		fmt.Printf("创建 bundle 失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ 成功同步 %d 个元数据文件到 %s\n", len(req.Files), *outFile)
}
