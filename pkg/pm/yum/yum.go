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

	sandbox := filepath.Join(metaDir, "sandbox")
	err := os.MkdirAll(sandbox, 0755)
	if err != nil {
		return nil, fmt.Errorf("创建沙盒目录失败: %v", err)
	}
	defer os.RemoveAll(sandbox) // 清理沙盒

	reposDir := filepath.Join(sandbox, "repos.d")
	err = os.MkdirAll(reposDir, 0755)
	if err != nil {
		return nil, fmt.Errorf("创建仓库配置目录失败: %v", err)
	}

	// 创建临时 repo 配置指向元数据目录
	repoFile := filepath.Join(reposDir, "local.repo")
	repoContent := fmt.Sprintf("[local-meta]\nname=Local Meta\nbaseurl=file://%s/\nenabled=1\ngpgcheck=0\n", strings.ReplaceAll(metaDir, "\\", "/"))
	err = os.WriteFile(repoFile, []byte(repoContent), 0644)
	if err != nil {
		return nil, fmt.Errorf("写入本地仓库配置失败: %v", err)
	}

	releaseVer := getReleaseVer()
	isDnf := strings.HasSuffix(m.cmdPath, "dnf")

	// 尝试多种策略获取包下载 URL
	var packages []core.PackageInfo

	if isDnf {
		// 策略 1: dnf repoquery --location（获取包本身及其依赖的下载 URL）
		packages, err = m.dnfRepoquery(sandbox, reposDir, releaseVer, targetPackages)
		if err != nil {
			// 策略 2: dnf download --resolve --url（部分 dnf 版本支持）
			packages, err = m.dnfDownloadURLs(sandbox, reposDir, releaseVer, targetPackages)
		}
	} else {
		// YUM: 使用 repoquery
		packages, err = m.yumRepoquery(sandbox, reposDir, targetPackages)
	}

	return packages, err
}

// dnfRepoquery 使用 dnf repoquery 获取包及依赖的下载 URL
func (m *YUMManager) dnfRepoquery(sandbox, reposDir, releaseVer string, targetPackages []string) ([]core.PackageInfo, error) {
	// 先获取目标包自身的 URL
	allURLs := make(map[string]bool)

	// 获取目标包及其所有依赖的 URL
	args := []string{
		"repoquery", "--location", "--resolve", "--requires", "--recursive",
		"--installroot=" + sandbox,
		"--releasever=" + releaseVer,
		"--setopt=reposdir=" + reposDir,
		"--disablerepo=*", "--enablerepo=local-meta",
	}
	args = append(args, targetPackages...)

	cmd := exec.Command(m.cmdPath, args...)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("dnf repoquery (依赖) 失败: %v\n  stderr: %s", err, stderr.String())
	}

	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if isPackageURL(line) {
			allURLs[line] = true
		}
	}

	// 再获取目标包本身的 URL
	args2 := []string{
		"repoquery", "--location",
		"--installroot=" + sandbox,
		"--releasever=" + releaseVer,
		"--setopt=reposdir=" + reposDir,
		"--disablerepo=*", "--enablerepo=local-meta",
	}
	args2 = append(args2, targetPackages...)

	cmd2 := exec.Command(m.cmdPath, args2...)
	var out2, stderr2 bytes.Buffer
	cmd2.Stdout = &out2
	cmd2.Stderr = &stderr2

	if err := cmd2.Run(); err == nil {
		for _, line := range strings.Split(out2.String(), "\n") {
			line = strings.TrimSpace(line)
			if isPackageURL(line) {
				allURLs[line] = true
			}
		}
	}

	return urlsToPackages(allURLs), nil
}

// dnfDownloadURLs 使用 dnf download --url --resolve 作为备选方案
func (m *YUMManager) dnfDownloadURLs(sandbox, reposDir, releaseVer string, targetPackages []string) ([]core.PackageInfo, error) {
	args := []string{
		"download", "--url", "--resolve",
		"--installroot=" + sandbox,
		"--releasever=" + releaseVer,
		"--setopt=reposdir=" + reposDir,
		"--disablerepo=*", "--enablerepo=local-meta",
	}
	args = append(args, targetPackages...)

	cmd := exec.Command(m.cmdPath, args...)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("dnf download --url 失败: %v\n  stderr: %s\n排障提示:\n  1. 请确认 dnf 版本支持 download 子命令\n  2. 尝试手动运行: dnf download --url --resolve <包名>", err, stderr.String())
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

// yumRepoquery 使用 repoquery 命令（适用于传统 YUM）
func (m *YUMManager) yumRepoquery(sandbox, reposDir string, targetPackages []string) ([]core.PackageInfo, error) {
	repoqueryPath, err := exec.LookPath("repoquery")
	if err != nil {
		return nil, fmt.Errorf("未找到 repoquery 命令\n排障提示: 请安装 yum-utils 包 (yum install -y yum-utils)")
	}

	args := []string{
		"--resolve", "--requires", "--recursive", "--location",
		"--installroot=" + sandbox,
		"--setopt=reposdir=" + reposDir,
	}
	args = append(args, targetPackages...)

	cmd := exec.Command(repoqueryPath, args...)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("repoquery 失败: %v\n  stderr: %s", err, stderr.String())
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
