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

	repoFile := filepath.Join(reposDir, "local.repo")
	repoContent := fmt.Sprintf("[local-meta]\nname=Local Meta\nbaseurl=file://%s/\nenabled=1\ngpgcheck=0\n", strings.ReplaceAll(metaDir, "\\", "/"))
	err = os.WriteFile(repoFile, []byte(repoContent), 0644)
	if err != nil {
		return nil, fmt.Errorf("写入本地仓库配置失败: %v", err)
	}

	releaseVer := getReleaseVer()
	var args []string
	if strings.HasSuffix(m.cmdPath, "dnf") {
		args = []string{
			"install", "--resolve", "--urls", "--installroot=" + sandbox,
			"--releasever=" + releaseVer, "--setopt=reposdir=" + reposDir,
			"--setopt=cachedir=" + filepath.Join(sandbox, "cache"),
		}
		args = append(args, targetPackages...)
	} else {
		args = []string{
			"repoquery", "--resolve", "--requires", "--recursive", "--location",
			"--installroot=" + sandbox,
			"--setopt=reposdir=" + reposDir,
		}
		args = append(args, targetPackages...)
	}

	var cmd *exec.Cmd
	if strings.HasSuffix(m.cmdPath, "dnf") {
		cmd = exec.Command(m.cmdPath, args...)
	} else {
		repoqueryPath, err := exec.LookPath("repoquery")
		if err != nil {
			return nil, fmt.Errorf("未找到 repoquery 命令")
		}
		cmd = exec.Command(repoqueryPath, args...)
	}
	
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("解析包依赖失败: %v, stderr: %s", err, stderr.String())
	}

	lines := strings.Split(out.String(), "\n")
	var packages []core.PackageInfo
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") || strings.HasPrefix(line, "file://") {
			filename := filepath.Base(line)
			packages = append(packages, core.PackageInfo{
				URL:      line,
				Filename: filename,
			})
		}
	}

	return packages, nil
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
