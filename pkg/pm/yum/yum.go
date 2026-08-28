package yum

import (
	"bufio"
	"bytes"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	"netlesspkg/pkg/core"
)

type YUMManager struct {
	cmdPath string
}

func init() {
	core.RegisterManager(&YUMManager{})
}

func (m *YUMManager) Name() string {
	return "yum"
}

func (m *YUMManager) Detect() bool {
	if _, err := os.Stat("/usr/bin/dnf"); err == nil {
		m.cmdPath = "/usr/bin/dnf"
		return true
	}
	if _, err := os.Stat("/usr/bin/yum"); err == nil {
		m.cmdPath = "/usr/bin/yum"
		return true
	}
	return false
}

func getReleaseVer() string {
	content, err := os.ReadFile("/etc/os-release")
	if err == nil {
		lines := strings.Split(string(content), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "VERSION_ID=") {
				ver := strings.TrimPrefix(line, "VERSION_ID=")
				ver = strings.Trim(ver, "\"'")
				return ver
			}
		}
	}
	out, err := exec.Command("rpm", "-E", "%{rhel}").Output()
	if err == nil {
		ver := strings.TrimSpace(string(out))
		if ver != "%{rhel}" {
			return ver
		}
	}
	return "7" // 默认 fallback
}

func getBaseArch() string {
	out, err := exec.Command("uname", "-m").Output()
	if err == nil {
		return strings.TrimSpace(string(out))
	}
	return "x86_64"
}

func fetchFirstMirror(mirrorlistURL string) string {
	resp, err := http.Get(mirrorlistURL)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			return line
		}
	}
	return ""
}

type repoConfig struct {
	enabled    bool
	baseurl    string
	mirrorlist string
}

// loadSystemRepoBaseURLs 解析 /etc/yum.repos.d/*.repo，获取所有已启用的 repo_id -> baseurl 映射
func loadSystemRepoBaseURLs() map[string]string {
	releaseVer := getReleaseVer()
	baseArch := getBaseArch()

	files, err := filepath.Glob("/etc/yum.repos.d/*.repo")
	if err != nil {
		return make(map[string]string)
	}

	repoURLs := make(map[string]string)
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}

		lines := strings.Split(string(content), "\n")
		var currentRepo string
		repos := make(map[string]*repoConfig)

		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}

			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				currentRepo = line[1 : len(line)-1]
				repos[currentRepo] = &repoConfig{enabled: true} // 默认启用
				continue
			}

			if currentRepo != "" {
				parts := strings.SplitN(line, "=", 2)
				if len(parts) != 2 {
					continue
				}
				key := strings.TrimSpace(parts[0])
				val := strings.TrimSpace(parts[1])

				val = strings.ReplaceAll(val, "$releasever", releaseVer)
				val = strings.ReplaceAll(val, "$basearch", baseArch)

				if key == "enabled" {
					repos[currentRepo].enabled = (val == "1")
				} else if key == "baseurl" {
					repos[currentRepo].baseurl = val
				} else if key == "mirrorlist" {
					repos[currentRepo].mirrorlist = val
				}
			}
		}

		for repoID, repo := range repos {
			if !repo.enabled {
				continue
			}

			baseURL := repo.baseurl
			if baseURL == "" && repo.mirrorlist != "" {
				baseURL = fetchFirstMirror(repo.mirrorlist)
			}

			if baseURL != "" {
				if !strings.HasSuffix(baseURL, "/") {
					baseURL += "/"
				}
				repoURLs[repoID] = baseURL
			}
		}
	}

	return repoURLs
}

func (m *YUMManager) GetMetadataURLs() ([]core.MetaFile, error) {
	repoURLs := loadSystemRepoBaseURLs()
	var metaFiles []core.MetaFile

	for repoID, baseURL := range repoURLs {
		repomdURL := baseURL + "repodata/repomd.xml"

		metaFiles = append(metaFiles, core.MetaFile{
			URL:      repomdURL,
			SavePath: repoID + "/repodata/repomd.xml",
			RepoID:   repoID,
			BaseURL:  baseURL,
		})
	}

	return metaFiles, nil
}

func (m *YUMManager) InjectMetaAndPlan(metaDir string, targetPackages []string) ([]core.PackageInfo, error) {
	if len(targetPackages) == 0 {
		return nil, nil
	}

	isDnf := strings.HasSuffix(m.cmdPath, "dnf")

	var packages []core.PackageInfo
	var err error
	var lastErr error

	// 策略 1: 使用从外网搬运回来的元数据构建本地离线 repo（推荐）
	fmt.Println("[plan] 策略 1: 使用搬运的离线元数据查询依赖...")
	packages, err = m.queryWithLocalRepos(metaDir, isDnf, targetPackages)
	if err == nil && len(packages) > 0 {
		return packages, nil
	}
	lastErr = err
	if err != nil {
		fmt.Printf("[plan] 策略 1 失败:\n%v\n\n", err)
	}

	if isDnf {
		// 策略 2: dnf -C repoquery（仅使用系统本地缓存）
		fmt.Println("[plan] 策略 2: 使用 dnf 系统缓存查询依赖...")
		packages, err = m.dnfRepoquerySystem(true, targetPackages)
		if err == nil && len(packages) > 0 {
			return packages, nil
		}
		lastErr = err
		if err != nil {
			fmt.Printf("[plan] 策略 2 失败:\n%v\n\n", err)
		}

		// 策略 3: dnf repoquery（允许网络刷新）
		fmt.Println("[plan] 策略 3: 使用 dnf repoquery（允许网络刷新）...")
		packages, err = m.dnfRepoquerySystem(false, targetPackages)
		if err == nil && len(packages) > 0 {
			return packages, nil
		}
		lastErr = err
		if err != nil {
			fmt.Printf("[plan] 策略 3 失败:\n%v\n\n", err)
		}
	} else {
		// 传统 YUM: repoquery
		fmt.Println("[plan] 策略 2: 使用 repoquery 查询依赖...")
		packages, err = m.yumRepoquerySystem(targetPackages)
		if err == nil && len(packages) > 0 {
			return packages, nil
		}
		lastErr = err
		if err != nil {
			fmt.Printf("[plan] 策略 2 失败:\n%v\n\n", err)
		}
	}

	if lastErr != nil {
		return nil, fmt.Errorf("所有依赖解析策略均失败\n最后一次错误: %v\n\n排障提示:\n"+
			"  1. 请确认系统 dnf/yum 元数据缓存存在: 尝试运行 dnf makecache 或 yum makecache\n"+
			"  2. 如果内网完全无法连接任何镜像源，请先在可达环境执行 dnf makecache 建立缓存\n"+
			"  3. 检查 /etc/yum.repos.d/ 下是否有可用的 repo 配置", lastErr)
	}

	return nil, fmt.Errorf("未找到任何需要下载的包")
}

// queryWithLocalRepos 使用从外网搬运回来的元数据创建本地离线 repo 进行依赖查询
func (m *YUMManager) queryWithLocalRepos(metaDir string, isDnf bool, targetPackages []string) ([]core.PackageInfo, error) {
	// 1. 扫描并自动组织 repo 目录结构
	repoIDs := make([]string, 0)

	// 检查子目录结构
	entries, err := os.ReadDir(metaDir)
	if err != nil {
		return nil, fmt.Errorf("读取元数据目录失败: %v", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			repoDataDir := filepath.Join(metaDir, entry.Name(), "repodata")
			if _, err := os.Stat(filepath.Join(repoDataDir, "repomd.xml")); err == nil {
				repoIDs = append(repoIDs, entry.Name())
			}
		} else if strings.HasSuffix(entry.Name(), "repomd.xml") {
			// 兼容旧版平铺结构：例如 mirrors.cloud.aliyuncs.com_alinux_3_os_aarch64_repodata_repomd.xml
			repoName := strings.TrimSuffix(entry.Name(), "_repomd.xml")
			repoName = strings.TrimSuffix(repoName, ".repomd.xml")
			targetDir := filepath.Join(metaDir, repoName, "repodata")
			os.MkdirAll(targetDir, 0755)
			srcPath := filepath.Join(metaDir, entry.Name())
			dstPath := filepath.Join(targetDir, "repomd.xml")
			// 复制或移动
			data, err := os.ReadFile(srcPath)
			if err == nil {
				os.WriteFile(dstPath, data, 0644)
				repoIDs = append(repoIDs, repoName)
			}
		}
	}

	if len(repoIDs) == 0 {
		return nil, fmt.Errorf("元数据包中未找到任何有效的 repomd.xml 文件。\n" +
			"排障提示：请在外网机器使用最新版本运行 sync-meta 下载完整的 metadata.bundle")
	}

	// 检查是否包含实际的包数据库 (primary.xml.gz 等)
	hasDatabaseFiles := false
	filepath.Walk(metaDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			name := info.Name()
			if strings.Contains(name, "primary") || strings.Contains(name, "filelists") ||
				strings.HasSuffix(name, ".xml.gz") || strings.HasSuffix(name, ".sqlite.bz2") {
				hasDatabaseFiles = true
			}
		}
		return nil
	})

	if !hasDatabaseFiles {
		return nil, fmt.Errorf("检测到 metadata.bundle 中仅包含仓库索引 (repomd.xml)，缺少核心依赖数据库 (primary.xml.gz)。\n" +
			"原因：旧版 sync-meta 仅拉取了索引文件，无法完成离线依赖计算。\n" +
			"解决方案：请在外网机器使用最新版 netlesspkg 重新执行:\n" +
			"  netlesspkg sync-meta -i meta_request.json -o metadata.bundle")
	}

	// 创建临时 repos.d 目录，为每个 repo 生成 .repo 配置
	tmpReposDir, err := os.MkdirTemp("", "netlesspkg-repos-*")
	if err != nil {
		return nil, fmt.Errorf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tmpReposDir)

	var repoConfigs []string
	for _, repoID := range repoIDs {
		repoPath := filepath.Join(metaDir, repoID)
		absPath, _ := filepath.Abs(repoPath)
		config := fmt.Sprintf("[%s]\nname=Offline %s\nbaseurl=file://%s/\nenabled=1\ngpgcheck=0\n\n",
			repoID, repoID, strings.ReplaceAll(absPath, "\\", "/"))
		repoConfigs = append(repoConfigs, config)
	}

	repoFile := filepath.Join(tmpReposDir, "offline.repo")
	err = os.WriteFile(repoFile, []byte(strings.Join(repoConfigs, "")), 0644)
	if err != nil {
		return nil, fmt.Errorf("写入临时 repo 配置失败: %v", err)
	}

	fmt.Printf("[plan] 已成功加载 %d 个离线仓库: %s\n", len(repoIDs), strings.Join(repoIDs, ", "))

	// 使用 dnf/yum 查询
	allURLs := make(map[string]bool)

	if isDnf {
		releaseVer := getReleaseVer()
		tmpCacheDir := filepath.Join(tmpReposDir, "cache")
		os.MkdirAll(tmpCacheDir, 0755)

		// 1. 增量依赖求解：以本地系统已有 RPM 为基准，仅下载真正缺失的依赖包
		fmt.Printf("[plan] 正在以本机环境为基准，增量计算 %s 的缺失依赖包...\n", strings.Join(targetPackages, ", "))

		neededPackages, err := m.resolveMissingDependencies(tmpReposDir, tmpCacheDir, releaseVer, repoIDs, targetPackages)
		if err != nil {
			fmt.Printf("[plan] 增量解析警告: %v，回退到全局依赖列表\n", err)
			neededPackages = targetPackages
		}

		fmt.Printf("[plan] 依赖分析完成，目标系统需安装 %d 个离线包（已自动排除系统已满足的底层库）\n", len(neededPackages))

		// 2. 查询所有确实需要下载的包的下载 URL
		argsURLs := []string{
			"--noplugins",
			"--setopt=reposdir=" + tmpReposDir,
			"--setopt=cachedir=" + tmpCacheDir,
			"--setopt=keepcache=1",
			"repoquery", "--location", "--latest-limit=1",
			"--disablerepo=*",
			"--releasever=" + releaseVer,
		}
		for _, id := range repoIDs {
			argsURLs = append(argsURLs, "--enablerepo="+id)
		}
		argsURLs = append(argsURLs, neededPackages...)

		cmdURLs := exec.Command(m.cmdPath, argsURLs...)
		var outURLs, stderrURLs bytes.Buffer
		cmdURLs.Stdout = &outURLs
		cmdURLs.Stderr = &stderrURLs

		if err := cmdURLs.Run(); err != nil {
			return nil, fmt.Errorf("dnf repoquery (下载链接查询) 失败: %v\n  stderr: %s", err, stderrURLs.String())
		}

		for _, line := range strings.Split(outURLs.String(), "\n") {
			line = strings.TrimSpace(line)
			if isPackageURL(line) {
				allURLs[line] = true
			}
		}
	} else {
		// 传统 YUM 递归查询依赖与 URL
		repoqueryPath, err := exec.LookPath("repoquery")
		if err != nil {
			return nil, fmt.Errorf("未找到 repoquery 命令")
		}

		// 1. 获取依赖包
		allPkgNames := make(map[string]bool)
		for _, pkg := range targetPackages {
			allPkgNames[pkg] = true
		}

		argsDeps := []string{
			"--plugins=0",
			"--resolve", "--requires", "--recursive",
			"--setopt=reposdir=" + tmpReposDir,
		}
		argsDeps = append(argsDeps, targetPackages...)
		cmdDeps := exec.Command(repoqueryPath, argsDeps...)
		var outDeps bytes.Buffer
		cmdDeps.Stdout = &outDeps
		if err := cmdDeps.Run(); err == nil {
			for _, line := range strings.Split(outDeps.String(), "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					// 过滤掉系统已安装的包
					if !isInstalledOnSystem(line) {
						allPkgNames[line] = true
					}
				}
			}
		}

		// 2. 批量查 URL
		pkgList := make([]string, 0, len(allPkgNames))
		for p := range allPkgNames {
			pkgList = append(pkgList, p)
		}

		argsURLs := []string{
			"--plugins=0",
			"--location", "--pkgnarrow=latest",
			"--setopt=reposdir=" + tmpReposDir,
		}
		argsURLs = append(argsURLs, pkgList...)
		cmdURLs := exec.Command(repoqueryPath, argsURLs...)
		var outURLs, stderrURLs bytes.Buffer
		cmdURLs.Stdout = &outURLs
		cmdURLs.Stderr = &stderrURLs
		if err := cmdURLs.Run(); err != nil {
			return nil, fmt.Errorf("repoquery (离线) 失败: %v\n  stderr: %s", err, stderrURLs.String())
		}
		for _, line := range strings.Split(outURLs.String(), "\n") {
			line = strings.TrimSpace(line)
			if isPackageURL(line) {
				allURLs[line] = true
			}
		}
	}

	if len(allURLs) == 0 {
		return nil, fmt.Errorf("离线元数据中未查询到包 URL（可能元数据不完整，请确认 sync-meta 步骤已下载全部元数据）")
	}

	repoBaseURLs := loadSystemRepoBaseURLs()
	return restoreRemoteURLs(allURLs, repoBaseURLs, metaDir), nil
}

// cacheOnly: 如果为 true 则使用 -C 仅查本地缓存
func (m *YUMManager) dnfRepoquerySystem(cacheOnly bool, targetPackages []string) ([]core.PackageInfo, error) {
	allURLs := make(map[string]bool)

	// 查询依赖的 URL
	args := []string{"repoquery", "--location", "--resolve", "--requires", "--recursive"}
	if cacheOnly {
		args = append(args, "-C", "--setopt=metadata_expire=-1")
	}
	args = append(args, targetPackages...)

	cmd := exec.Command(m.cmdPath, args...)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("dnf repoquery --requires 失败: %v\n  stderr: %s", err, firstLines(stderr.String(), 5))
	}

	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if isPackageURL(line) {
			allURLs[line] = true
		}
	}

	// 查询目标包本身的 URL
	args2 := []string{"repoquery", "--location"}
	if cacheOnly {
		args2 = append(args2, "-C", "--setopt=metadata_expire=-1")
	}
	args2 = append(args2, targetPackages...)

	cmd2 := exec.Command(m.cmdPath, args2...)
	var out2 bytes.Buffer
	cmd2.Stdout = &out2

	if err := cmd2.Run(); err == nil {
		for _, line := range strings.Split(out2.String(), "\n") {
			line = strings.TrimSpace(line)
			if isPackageURL(line) {
				allURLs[line] = true
			}
		}
	}

	if len(allURLs) == 0 {
		return nil, fmt.Errorf("未查询到任何包的下载 URL")
	}

	return urlsToPackages(allURLs), nil
}

// dnfDownloadSystem 使用 dnf download --url --resolve 查询下载 URL
func (m *YUMManager) dnfDownloadSystem(targetPackages []string) ([]core.PackageInfo, error) {
	args := []string{"download", "--url", "--resolve"}
	args = append(args, targetPackages...)

	cmd := exec.Command(m.cmdPath, args...)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("dnf download --url 失败: %v\n  stderr: %s", err, firstLines(stderr.String(), 5))
	}

	allURLs := make(map[string]bool)
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if isPackageURL(line) {
			allURLs[line] = true
		}
	}

	return urlsToPackages(allURLs), nil
}

// yumRepoquerySystem 使用系统原生 repoquery 查询
func (m *YUMManager) yumRepoquerySystem(targetPackages []string) ([]core.PackageInfo, error) {
	repoqueryPath, err := exec.LookPath("repoquery")
	if err != nil {
		return nil, fmt.Errorf("未找到 repoquery 命令\n排障提示: 请安装 yum-utils 包 (yum install -y yum-utils)")
	}

	args := []string{"--resolve", "--requires", "--recursive", "--location"}
	args = append(args, targetPackages...)

	cmd := exec.Command(repoqueryPath, args...)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("repoquery 失败: %v\n  stderr: %s", err, firstLines(stderr.String(), 5))
	}

	allURLs := make(map[string]bool)
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if isPackageURL(line) {
			allURLs[line] = true
		}
	}

	return urlsToPackages(allURLs), nil
}

// isPackageURL 判断是否为有效的包下载 URL
func isPackageURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "file://")
}

// restoreRemoteURLs 将 DNF/YUM 返回的 local file:// URL 还原为外网可下载的真实 HTTP/HTTPS 镜像 URL
func restoreRemoteURLs(urls map[string]bool, repoBaseURLs map[string]string, metaDir string) []core.PackageInfo {
	var packages []core.PackageInfo

	for rawURL := range urls {
		finalURL := rawURL
		filename := filepath.Base(rawURL)

		if strings.HasPrefix(rawURL, "file://") {
			cleanPath := strings.TrimPrefix(rawURL, "file://")
			cleanPath = strings.ReplaceAll(cleanPath, "\\", "/")

			matched := false
			// 1. 精确匹配 repo_id
			for repoID, baseURL := range repoBaseURLs {
				pattern := "/" + repoID + "/"
				if idx := strings.Index(cleanPath, pattern); idx != -1 {
					subPath := cleanPath[idx+len(pattern):]
					if !strings.HasSuffix(baseURL, "/") {
						baseURL += "/"
					}
					finalURL = baseURL + strings.TrimPrefix(subPath, "/")
					matched = true
					break
				}
			}

			// 2. 模糊匹配 (如果 repo_id 包含在路径中)
			if !matched {
				for repoID, baseURL := range repoBaseURLs {
					if strings.Contains(cleanPath, repoID) {
						idx := strings.Index(cleanPath, repoID)
						subPath := cleanPath[idx+len(repoID):]
						if !strings.HasSuffix(baseURL, "/") {
							baseURL += "/"
						}
						finalURL = baseURL + strings.TrimPrefix(subPath, "/")
						matched = true
						break
					}
				}
			}
		}

		packages = append(packages, core.PackageInfo{
			URL:      finalURL,
			Filename: filename,
		})
	}

	return packages
}

// urlsToPackages 将 URL 集合转换为 PackageInfo 列表
func urlsToPackages(urls map[string]bool) []core.PackageInfo {
	var packages []core.PackageInfo
	for url := range urls {
		filename := filepath.Base(url)
		packages = append(packages, core.PackageInfo{
			URL:      url,
			Filename: filename,
		})
	}
	return packages
}

// firstLines 取错误输出的前 N 行，避免超长输出
func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + "\n  ..."
}

// summarizeError 取错误信息的第一行用于简短展示
func summarizeError(err error) string {
	s := err.Error()
	if idx := strings.Index(s, "\n"); idx > 0 {
		return s[:idx]
	}
	return s
}

func (m *YUMManager) InjectPackagesAndInstall(pkgDir string, targetPackages []string) error {
	currentUser, err := user.Current()
	if err != nil {
		return fmt.Errorf("获取当前用户失败: %v", err)
	}
	if currentUser.Uid != "0" {
		return fmt.Errorf("需要 root 权限才能安装软件包")
	}

	if m.cmdPath == "" {
		return fmt.Errorf("包管理器未初始化")
	}

	cleanDir := strings.ReplaceAll(pkgDir, "\\", "/")
	rpmFiles, _ := filepath.Glob(filepath.Join(pkgDir, "*.rpm"))
	if len(rpmFiles) == 0 {
		return fmt.Errorf("在解压目录中未找到任何 .rpm 文件: %s", pkgDir)
	}

	fmt.Printf("[install] 正在准备安装 %d 个 RPM 离线包...\n", len(rpmFiles))

	// 策略 1: dnf/yum install 配合 --disablerepo=* 禁用全部远端源，仅基于本地 RPM 文件计算依赖安装
	cmdStr1 := fmt.Sprintf("%s install -y --nogpgcheck --noplugins --disablerepo=* %s/*.rpm", m.cmdPath, cleanDir)
	fmt.Printf("[install] 策略 1: %s\n", cmdStr1)
	cmd1 := exec.Command("sh", "-c", cmdStr1)
	cmd1.Stdout = os.Stdout
	cmd1.Stderr = os.Stderr
	if err1 := cmd1.Run(); err1 == nil {
		fmt.Println("\n✅ 软件包离线安装成功！")
		return nil
	}

	// 策略 2: 使用 rpm 原生命令批量安装/更新
	fmt.Println("\n[install] 策略 2: 尝试使用 rpm 命令直接安装...")
	cmdStr2 := fmt.Sprintf("rpm -Uvh --replacepkgs --replacefiles %s/*.rpm", cleanDir)
	fmt.Printf("[install] 执行: %s\n", cmdStr2)
	cmd2 := exec.Command("sh", "-c", cmdStr2)
	cmd2.Stdout = os.Stdout
	cmd2.Stderr = os.Stderr
	if err2 := cmd2.Run(); err2 == nil {
		fmt.Println("\n✅ 软件包离线安装成功！")
		return nil
	}

	return fmt.Errorf("所有离线安装策略均失败，请查看上方输出排查是否存在缺失的底层系统依赖")
}

// resolveMissingDependencies 以本机已安装 RPM 状态为基准，BFS 递归求解目标包真正缺失的依赖包
func (m *YUMManager) resolveMissingDependencies(tmpReposDir, tmpCacheDir, releaseVer string, repoIDs []string, targetPackages []string) ([]string, error) {
	installedPkgs, installedProvides := getSystemInstalledCapabilities()

	neededPackages := make(map[string]bool)
	queue := make([]string, 0)

	for _, pkg := range targetPackages {
		neededPackages[pkg] = true
		queue = append(queue, pkg)
	}

	visitedPackages := make(map[string]bool)
	checkedRequires := make(map[string]bool)

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if visitedPackages[current] {
			continue
		}
		visitedPackages[current] = true

		// 查询当前包的直接 requirements
		args := []string{
			"--noplugins",
			"--setopt=reposdir=" + tmpReposDir,
			"--setopt=cachedir=" + tmpCacheDir,
			"repoquery", "--requires",
			"--disablerepo=*",
			"--releasever=" + releaseVer,
		}
		for _, id := range repoIDs {
			args = append(args, "--enablerepo="+id)
		}
		args = append(args, current)

		out, err := exec.Command(m.cmdPath, args...).Output()
		if err != nil {
			continue
		}

		missingRequires := make([]string, 0)
		for _, req := range strings.Split(string(out), "\n") {
			req = strings.TrimSpace(req)
			if req == "" || strings.HasPrefix(req, "#") {
				continue
			}
			// 过滤掉 rpmlib 内部能力 (如 rpmlib(PayloadFilesHavePrefix) 等)
			if strings.HasPrefix(req, "rpmlib(") {
				continue
			}

			if checkedRequires[req] {
				continue
			}
			checkedRequires[req] = true

			// 检查本地系统是否已经满足
			reqName := req
			if idx := strings.Index(req, " "); idx != -1 {
				reqName = req[:idx]
			}
			if installedProvides[req] || installedProvides[reqName] || installedPkgs[reqName] {
				// 本地已满足，跳过！不向下递归！
				continue
			}

			// 进一步使用 rpm -q --whatprovides 二次确认
			if err := exec.Command("rpm", "-q", "--whatprovides", req).Run(); err == nil {
				installedProvides[req] = true
				continue
			}

			missingRequires = append(missingRequires, req)
		}

		if len(missingRequires) == 0 {
			continue
		}

		// 在离线仓库中查询是谁提供了这些 missing requires
		argsWhat := []string{
			"--noplugins",
			"--setopt=reposdir=" + tmpReposDir,
			"--setopt=cachedir=" + tmpCacheDir,
			"repoquery", "--whatprovides",
			"--disablerepo=*",
			"--releasever=" + releaseVer,
		}
		for _, id := range repoIDs {
			argsWhat = append(argsWhat, "--enablerepo="+id)
		}
		argsWhat = append(argsWhat, missingRequires...)

		outProviders, err := exec.Command(m.cmdPath, argsWhat...).Output()
		if err != nil {
			continue
		}

		for _, provider := range strings.Split(string(outProviders), "\n") {
			provider = strings.TrimSpace(provider)
			if provider == "" || strings.HasPrefix(provider, "#") {
				continue
			}
			if !neededPackages[provider] {
				neededPackages[provider] = true
				queue = append(queue, provider)
			}
		}
	}

	result := make([]string, 0, len(neededPackages))
	for pkg := range neededPackages {
		result = append(result, pkg)
	}

	return result, nil
}

// getSystemInstalledCapabilities 获取本机已安装的全部 RPM 包名和 Provides
func getSystemInstalledCapabilities() (map[string]bool, map[string]bool) {
	installedPkgs := make(map[string]bool)
	installedProvides := make(map[string]bool)

	// 1. 获取已安装包名
	outPkgs, err := exec.Command("rpm", "-qa", "--qf", "%{NAME}\n").Output()
	if err == nil {
		for _, line := range strings.Split(string(outPkgs), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				installedPkgs[line] = true
			}
		}
	}

	// 2. 获取已安装 provides
	outProvides, err := exec.Command("rpm", "-qa", "--provides").Output()
	if err == nil {
		for _, line := range strings.Split(string(outProvides), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				installedProvides[line] = true
				if idx := strings.Index(line, " "); idx != -1 {
					installedProvides[line[:idx]] = true
				}
				if idx := strings.Index(line, "="); idx != -1 {
					installedProvides[line[:idx]] = true
				}
			}
		}
	}

	return installedPkgs, installedProvides
}

// isInstalledOnSystem 检查系统是否已经安装了该包
func isInstalledOnSystem(pkgName string) bool {
	err := exec.Command("rpm", "-q", pkgName).Run()
	return err == nil
}
