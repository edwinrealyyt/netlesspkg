package cmd

import (
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"netlesspkg/pkg/archive"
	"netlesspkg/pkg/core"
	"netlesspkg/pkg/downloader"
	"os"
	"path/filepath"
	"strings"
)

// repomd XML 结构（用于解析 YUM repomd.xml）
type repomd struct {
	XMLName xml.Name   `xml:"repomd"`
	Data    []repoData `xml:"data"`
}

type repoData struct {
	Type     string       `xml:"type,attr"`
	Location repoLocation `xml:"location"`
	Size     int64        `xml:"size"`
}

type repoLocation struct {
	Href string `xml:"href,attr"`
}

func runSyncMeta(args []string) {
	fs := flag.NewFlagSet("sync-meta", flag.ContinueOnError)
	inFile := fs.String("i", "meta_request.json", "输入文件路径")
	outFile := fs.String("o", "metadata.bundle", "输出 bundle 路径")
	noAutoReplace := fs.Bool("no-auto-replace", false, "禁用内置云厂商内网源自动映射为公网源")

	var replaceRules stringSliceFlag
	fs.Var(&replaceRules, "replace", "URL 替换规则，格式为 <旧地址>=<新地址>，支持指定多次")
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

	// 第 1 轮：下载所有 repomd.xml 和其他元数据
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
			SavePath: filepath.Join(tmpDir, filepath.FromSlash(f.SavePath)),
		})
	}

	if replacedCount > 3 {
		fmt.Printf("[URL 重写] 共计重写了 %d 个内网源 URL 为公网镜像源\n", replacedCount)
	}

	fmt.Printf("正在下载 %d 个元数据文件...\n", len(tasks))
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

	// 第 2 轮：解析 YUM repomd.xml，下载引用的附加元数据 (primary.xml.gz 等)
	if req.OSFamily == "rhel" {
		extraTasks := parseAndCollectRepomdFiles(req.Files, tmpDir, rewriter)
		if len(extraTasks) > 0 {
			fmt.Printf("\n正在下载 %d 个附加元数据文件 (primary, filelists 等)...\n", len(extraTasks))
			extraResults := downloader.Download(extraTasks, downloader.Options{
				Concurrency:  4,
				RetryCount:   3,
				EnableResume: true,
				ShowProgress: true,
			})

			extraFail := 0
			for _, r := range extraResults {
				if r.Error != nil {
					fmt.Printf("❌ 下载附加元数据失败: %s - %v\n", r.Task.URL, r.Error)
					extraFail++
				}
			}
			if extraFail > 0 {
				fmt.Printf("警告: %d 个附加元数据文件下载失败，可能影响 plan 步骤的依赖解析\n", extraFail)
			}
		}
	}

	if err := archive.CreateBundle(*outFile, tmpDir); err != nil {
		fmt.Printf("创建 bundle 失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ 成功同步元数据到 %s\n", *outFile)
}

// parseAndCollectRepomdFiles 解析已下载的 repomd.xml 文件，收集引用的附加元数据下载任务
func parseAndCollectRepomdFiles(files []core.MetaFile, tmpDir string, rewriter *core.URLRewriter) []downloader.DownloadTask {
	var tasks []downloader.DownloadTask
	seen := make(map[string]bool)

	for _, f := range files {
		// 只处理 repomd.xml 文件
		if f.BaseURL == "" || f.RepoID == "" {
			continue
		}
		if !strings.HasSuffix(f.SavePath, "repomd.xml") {
			continue
		}

		repomdPath := filepath.Join(tmpDir, filepath.FromSlash(f.SavePath))
		xmlData, err := os.ReadFile(repomdPath)
		if err != nil {
			fmt.Printf("警告: 无法读取 %s: %v\n", repomdPath, err)
			continue
		}

		var rmd repomd
		if err := xml.Unmarshal(xmlData, &rmd); err != nil {
			fmt.Printf("警告: 解析 %s 失败: %v\n", repomdPath, err)
			continue
		}

		for _, d := range rmd.Data {
			if d.Location.Href == "" {
				continue
			}

			// 构建完整 URL: baseURL + location.href
			fullURL := f.BaseURL + d.Location.Href
			downloadURL, _, _ := rewriter.RewriteURL(fullURL)

			// save_path: {repo_id}/{location.href}
			savePath := filepath.Join(tmpDir, f.RepoID, filepath.FromSlash(d.Location.Href))

			if seen[downloadURL] {
				continue
			}
			seen[downloadURL] = true

			tasks = append(tasks, downloader.DownloadTask{
				URL:      downloadURL,
				SavePath: savePath,
				Size:     d.Size,
			})
		}
	}

	return tasks
}
