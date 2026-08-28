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

func (m *YUMManager) GetMetadataURLs() ([]core.MetaFile, error) {
	releaseVer := getReleaseVer()
	baseArch := getBaseArch()

	files, err := filepath.Glob("/etc/yum.repos.d/*.repo")
	if err != nil {
		return nil, fmt.Errorf("读取 /etc/yum.repos.d/ 失败: %v", err)
	}

	var metaFiles []core.MetaFile
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

		for _, repo := range repos {
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
				repomdURL := baseURL + "repodata/repomd.xml"
				
				parts := strings.SplitN(repomdURL, "://", 2)
				var savePath string
				if len(parts) == 2 {
					savePath = strings.ReplaceAll(parts[1], "/", "_")
				} else {
					savePath = strings.ReplaceAll(repomdURL, "/", "_")
				}
				
				metaFiles = append(metaFiles, core.MetaFile{
					URL:      repomdURL,
					SavePath: savePath,
				})
			}
		}
	}

	return metaFiles, nil
}

func (m *YUMManager) InjectMetaAndPlan(metaDir string, targetPackages []string) ([]core.PackageInfo, error) {
	if len(targetPackages) == 0 {
		return nil, nil
	}

	// plan 步骤在内网执行，内网机器已有 repo 配置和元数据缓存。
	// 直接使用系统原生 dnf/yum 查询依赖即可，不需要创建沙盒 repo。
	// metaDir 参数在 YUM 场景下暂不使用（系统缓存已足够）。

	isDnf := strings.HasSuffix(m.cmdPath, "dnf")

	var packages []core.PackageInfo
	var err error
	var lastErr error

	if isDnf {
		// 策略 1: dnf -C repoquery（仅使用本地缓存，不访问网络）
		fmt.Println("[plan] 策略 1: 使用 dnf 本地缓存查询依赖...")
		packages, err = m.dnfRepoquerySystem(true, targetPackages)
		if err == nil && len(packages) > 0 {
			return packages, nil
		}
		lastErr = err
		if err != nil {
			fmt.Printf("[plan] 策略 1 失败: %v\n", summarizeError(err))
		}

		// 策略 2: dnf repoquery（允许刷新元数据，部分 repo 可能内网可达）
		fmt.Println("[plan] 策略 2: 使用 dnf repoquery（允许网络刷新）...")
		packages, err = m.dnfRepoquerySystem(false, targetPackages)
		if err == nil && len(packages) > 0 {
			return packages, nil
		}
		lastErr = err
		if err != nil {
			fmt.Printf("[plan] 策略 2 失败: %v\n", summarizeError(err))
		}

		// 策略 3: dnf download --url --resolve
		fmt.Println("[plan] 策略 3: 使用 dnf download --url --resolve...")
		packages, err = m.dnfDownloadSystem(targetPackages)
		if err == nil && len(packages) > 0 {
			return packages, nil
		}
		lastErr = err
	} else {
		// 传统 YUM: repoquery
		fmt.Println("[plan] 使用 repoquery 查询依赖...")
		packages, err = m.yumRepoquerySystem(targetPackages)
		if err == nil && len(packages) > 0 {
			return packages, nil
		}
		lastErr = err
	}

	if lastErr != nil {
		return nil, fmt.Errorf("所有依赖解析策略均失败\n最后一次错误: %v\n\n排障提示:\n"+
			"  1. 请确认系统 dnf/yum 元数据缓存存在: 尝试运行 dnf makecache 或 yum makecache\n"+
			"  2. 如果内网完全无法连接任何镜像源，请先在可达环境执行 dnf makecache 建立缓存\n"+
			"  3. 检查 /etc/yum.repos.d/ 下是否有可用的 repo 配置", lastErr)
	}

	return nil, fmt.Errorf("未找到任何需要下载的包")
}

// dnfRepoquerySystem 使用系统原生 dnf repoquery 查询包及依赖的下载 URL
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

	args := []string{"-c", fmt.Sprintf("%s install -y %s/*.rpm", m.cmdPath, strings.ReplaceAll(pkgDir, "\\", "/"))}
	cmd := exec.Command("sh", args...)
	
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	
	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("安装软件包失败: %v, stderr: %s", err, stderr.String())
	}

	return nil
}
