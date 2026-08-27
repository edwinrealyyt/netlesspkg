package apt

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"netlesspkg/pkg/core"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

func init() {
	core.RegisterManager(&APTManager{})
}

type APTManager struct{}

func (m *APTManager) Name() string {
	return "apt"
}

func (m *APTManager) Detect() bool {
	_, err := os.Stat("/usr/bin/apt-get")
	return err == nil
}

func getArchitectures() ([]string, error) {
	archs := []string{}
	
	// 主架构
	cmd := exec.Command("dpkg", "--print-architecture")
	out, err := cmd.Output()
	if err == nil {
		arch := strings.TrimSpace(string(out))
		if arch != "" {
			archs = append(archs, arch)
		}
	}

	// 辅助架构
	cmd = exec.Command("dpkg", "--print-foreign-architectures")
	out, err = cmd.Output()
	if err == nil {
		lines := strings.Split(string(out), "\n")
		for _, line := range lines {
			arch := strings.TrimSpace(line)
			if arch != "" {
				archs = append(archs, arch)
			}
		}
	}

	if len(archs) == 0 {
		return nil, fmt.Errorf("无法获取系统架构")
	}
	return archs, nil
}

func (m *APTManager) GetMetadataURLs() ([]core.MetaFile, error) {
	archs, err := getArchitectures()
	if err != nil {
		return nil, err
	}

	var metaFiles []core.MetaFile
	urlMap := make(map[string]string) // url -> save_path，去重用

	addMetaFile := func(url, savePath string) {
		if _, exists := urlMap[url]; !exists {
			urlMap[url] = savePath
			metaFiles = append(metaFiles, core.MetaFile{URL: url, SavePath: savePath})
		}
	}

	processSource := func(baseURL, codename string, components []string) {
		baseURL = strings.TrimRight(baseURL, "/")
		
		// InRelease, Release, Release.gpg
		baseReleasePath := strings.ReplaceAll(strings.TrimPrefix(strings.TrimPrefix(baseURL, "http://"), "https://"), "/", "_")
		
		addMetaFile(fmt.Sprintf("%s/dists/%s/InRelease", baseURL, codename), 
			fmt.Sprintf("%s_dists_%s_InRelease", baseReleasePath, codename))
		addMetaFile(fmt.Sprintf("%s/dists/%s/Release", baseURL, codename), 
			fmt.Sprintf("%s_dists_%s_Release", baseReleasePath, codename))
		addMetaFile(fmt.Sprintf("%s/dists/%s/Release.gpg", baseURL, codename), 
			fmt.Sprintf("%s_dists_%s_Release.gpg", baseReleasePath, codename))

		for _, comp := range components {
			for _, arch := range archs {
				pkgURL := fmt.Sprintf("%s/dists/%s/%s/binary-%s/Packages.gz", baseURL, codename, comp, arch)
				savePath := fmt.Sprintf("%s_dists_%s_%s_binary-%s_Packages", baseReleasePath, codename, comp, arch)
				addMetaFile(pkgURL, savePath)
			}
		}
	}

	// 1. 传统 one-line 格式
	parseOneLineFile := func(path string) error {
		file, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}

			parts := strings.Fields(line)
			if len(parts) >= 4 && parts[0] == "deb" {
				// 处理可能的 [arch=amd64] 选项
				var baseURL, codename string
				var components []string
				
				idx := 1
				if strings.HasPrefix(parts[idx], "[") {
					for idx < len(parts) && !strings.HasSuffix(parts[idx], "]") {
						idx++
					}
					idx++
				}
				
				if idx+1 < len(parts) {
					baseURL = parts[idx]
					codename = parts[idx+1]
					components = parts[idx+2:]
					processSource(baseURL, codename, components)
				}
			}
		}
		return scanner.Err()
	}

	parseOneLineFile("/etc/apt/sources.list")
	
	files, _ := filepath.Glob("/etc/apt/sources.list.d/*.list")
	for _, f := range files {
		parseOneLineFile(f)
	}

	// 2. DEB822 格式
	parseDeb822File := func(path string) error {
		file, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		defer file.Close()

		content, err := io.ReadAll(file)
		if err != nil {
			return err
		}

		stanzas := strings.Split(string(content), "\n\n")
		for _, stanza := range stanzas {
			lines := strings.Split(stanza, "\n")
			var types, uris, suites, components []string
			
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					key := strings.TrimSpace(parts[0])
					val := strings.TrimSpace(parts[1])
					
					switch key {
					case "Types":
						types = strings.Fields(val)
					case "URIs":
						uris = strings.Fields(val)
					case "Suites":
						suites = strings.Fields(val)
					case "Components":
						components = strings.Fields(val)
					}
				}
			}
			
			hasDeb := false
			for _, t := range types {
				if t == "deb" {
					hasDeb = true
					break
				}
			}
			
			if hasDeb {
				for _, uri := range uris {
					for _, suite := range suites {
						processSource(uri, suite, components)
					}
				}
			}
		}
		return nil
	}

	files, _ = filepath.Glob("/etc/apt/sources.list.d/*.sources")
	for _, f := range files {
		parseDeb822File(f)
	}

	return metaFiles, nil
}

func (m *APTManager) InjectMetaAndPlan(metaDir string, targetPackages []string) ([]core.PackageInfo, error) {
	if len(targetPackages) == 0 {
		return nil, nil
	}

	sandbox, err := os.MkdirTemp("", "apt-sandbox-*")
	if err != nil {
		return nil, fmt.Errorf("创建 sandbox 失败: %v", err)
	}
	defer os.RemoveAll(sandbox)

	listsDir := filepath.Join(sandbox, "lists")
	statusFile := filepath.Join(sandbox, "status")
	cacheDir := filepath.Join(sandbox, "cache")

	os.MkdirAll(listsDir, 0755)
	os.MkdirAll(cacheDir, 0755)
	os.WriteFile(statusFile, []byte(""), 0644)

	// 复制或链接 metaDir 中的文件到 listsDir
	metaFiles, err := os.ReadDir(metaDir)
	if err == nil {
		for _, mf := range metaFiles {
			if !mf.IsDir() {
				src := filepath.Join(metaDir, mf.Name())
				dst := filepath.Join(listsDir, mf.Name())
				// 尝试软链接，失败则复制 (在Windows开发但目标为Linux，这里实现复制)
				content, err := os.ReadFile(src)
				if err == nil {
					os.WriteFile(dst, content, 0644)
				}
			}
		}
	}

	args := []string{
		"install", "--print-uris", "-qq",
		"-o", "Dir::State::Lists=" + listsDir,
		"-o", "Dir::State::status=" + statusFile,
		"-o", "Dir::Cache=" + cacheDir,
		"-o", "Debug::NoLocking=1",
		"-o", "Acquire::AllowInsecureRepositories=true",
		"-o", "APT::Get::AllowUnauthenticated=true",
	}
	args = append(args, targetPackages...)

	cmd := exec.Command("apt-get", args...)
	out, err := cmd.Output()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("apt-get 规划失败: %s", string(exitError.Stderr))
		}
		return nil, fmt.Errorf("执行 apt-get 失败: %v", err)
	}

	var packages []core.PackageInfo
	lines := strings.Split(string(out), "\n")
	
	// 'URL' filename size SHA256:hash
	re := regexp.MustCompile(`'([^']+)'\s+(\S+)\s+(\d+)\s+SHA256:(\S+)`)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		
		matches := re.FindStringSubmatch(line)
		if len(matches) == 5 {
			size, _ := strconv.ParseInt(matches[3], 10, 64)
			packages = append(packages, core.PackageInfo{
				URL:      matches[1],
				Filename: matches[2],
				Size:     size,
				SHA256:   matches[4],
			})
		}
	}

	return packages, nil
}

func (m *APTManager) InjectPackagesAndInstall(pkgDir string, targetPackages []string) error {
	if os.Getuid() != 0 {
		return fmt.Errorf("需要 root 权限执行安装")
	}

	if len(targetPackages) == 0 {
		return nil
	}

	aptCacheDir := "/var/cache/apt/archives/"
	err := os.MkdirAll(aptCacheDir, 0755)
	if err != nil {
		return fmt.Errorf("创建 APT 缓存目录失败: %v", err)
	}

	// 复制 pkgDir 中的 deb 到 cache
	debs, err := os.ReadDir(pkgDir)
	if err == nil {
		for _, deb := range debs {
			if !deb.IsDir() && strings.HasSuffix(deb.Name(), ".deb") {
				src := filepath.Join(pkgDir, deb.Name())
				dst := filepath.Join(aptCacheDir, deb.Name())
				content, err := os.ReadFile(src)
				if err == nil {
					os.WriteFile(dst, content, 0644)
				}
			}
		}
	}

	args := append([]string{"install", "-y", "--allow-unauthenticated"}, targetPackages...)
	cmd := exec.Command("apt-get", args...)
	
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	
	if err := cmd.Run(); err != nil {
		// 失败则尝试修复
		fixCmd := exec.Command("apt-get", "install", "-f", "-y")
		if fixErr := fixCmd.Run(); fixErr != nil {
			return fmt.Errorf("安装失败，且尝试修复依赖也失败。第一次错误: %s", stderr.String())
		}
		// 修复后再次尝试安装
		cmd2 := exec.Command("apt-get", args...)
		var stderr2 bytes.Buffer
		cmd2.Stderr = &stderr2
		if err2 := cmd2.Run(); err2 != nil {
			return fmt.Errorf("修复依赖后依然安装失败: %s", stderr2.String())
		}
	}

	return nil
}
