package cmd

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"net/url"
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

	// 规范化 MetaFile 列表（兼容旧版 meta_request.json）
	normalizedFiles := make([]core.MetaFile, 0, len(req.Files))
	for _, f := range req.Files {
		norm := f
		if req.OSFamily == "rhel" {
			if norm.BaseURL == "" && strings.Contains(norm.URL, "repodata/repomd.xml") {
				norm.BaseURL = norm.URL[:strings.Index(norm.URL, "repodata/repomd.xml")]
			}
			if norm.RepoID == "" {
				norm.RepoID = inferRepoID(norm.URL, norm.SavePath)
			}
			// 统一组织为 {repo_id}/repodata/repomd.xml 目录结构
			norm.SavePath = filepath.Join(norm.RepoID, "repodata", "repomd.xml")
		}
		normalizedFiles = append(normalizedFiles, norm)
	}

	// 第 1 轮：下载所有索引元数据 (repomd.xml / Packages.gz 等)
	var tasks []downloader.DownloadTask
	replacedCount := 0

	for _, f := range normalizedFiles {
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

	fmt.Printf("正在下载 %d 个索引元数据文件...\n", len(tasks))
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

	// 第 2 轮：解析 YUM repomd.xml，递归下载引用的附加元数据 (primary.xml.gz, filelists 等)
	if req.OSFamily == "rhel" {
		extraTasks := parseAndCollectRepomdFiles(normalizedFiles, tmpDir, rewriter)
		if len(extraTasks) > 0 {
			fmt.Printf("\n正在下载 %d 个核心包依赖数据库 (primary.xml, filelists 等)...\n", len(extraTasks))
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

	fmt.Printf("\n✅ 成功同步全量元数据到 %s\n", *outFile)
}

// inferRepoID 从 URL 或 SavePath 推导一个可读的 Repo ID
func inferRepoID(rawURL, savePath string) string {
	u, err := url.Parse(rawURL)
	if err == nil && u.Path != "" {
		p := strings.Trim(u.Path, "/")
		p = strings.TrimSuffix(p, "/repodata/repomd.xml")
		p = strings.TrimSuffix(p, "repodata/repomd.xml")
		parts := strings.Split(p, "/")
		if len(parts) >= 2 {
			return strings.Join(parts[len(parts)-2:], "-")
		} else if len(parts) == 1 && parts[0] != "" {
			return parts[0]
		}
	}
	hash := md5.Sum([]byte(rawURL))
	return "repo-" + hex.EncodeToString(hash[:4])
}

// parseAndCollectRepomdFiles 解析已下载的 repomd.xml 文件，收集引用的附加元数据下载任务
func parseAndCollectRepomdFiles(files []core.MetaFile, tmpDir string, rewriter *core.URLRewriter) []downloader.DownloadTask {
	var tasks []downloader.DownloadTask
	seen := make(map[string]bool)

	for _, f := range files {
		if f.BaseURL == "" || f.RepoID == "" {
			continue
		}

		repomdPath := filepath.Join(tmpDir, filepath.FromSlash(f.SavePath))
		xmlData, err := os.ReadFile(repomdPath)
		if err != nil {
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
			base := f.BaseURL
			if !strings.HasSuffix(base, "/") {
				base += "/"
			}
			fullURL := base + strings.TrimPrefix(d.Location.Href, "/")
			downloadURL, _, _ := rewriter.RewriteURL(fullURL)

			// save_path 必须对应仓库的 {repo_id}/{location.href}
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
