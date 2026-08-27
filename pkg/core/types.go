// Package core 定义 NetlessPkg 的核心接口与数据结构。
package core

// MetaFile 表示一个需要下载的元数据文件。
type MetaFile struct {
	URL      string `json:"url"`       // 外网下载地址
	SavePath string `json:"save_path"` // 注入到内网时，相对于缓存根目录的路径
}

// MetaRequest 表示 export 阶段的输出 (meta_request.json)。
type MetaRequest struct {
	OSFamily string     `json:"os_family"` // "debian" 或 "rhel"
	Arch     string     `json:"arch"`      // 如 "amd64", "x86_64"
	Files    []MetaFile `json:"files"`
}

// PackageInfo 表示一个需要下载的安装包，包含完整性校验信息。
type PackageInfo struct {
	URL      string `json:"url"`
	Filename string `json:"filename"`
	SHA256   string `json:"sha256,omitempty"` // 用于下载后的完整性校验
	Size     int64  `json:"size"`
}

// DownloadPlan 表示 plan 阶段的输出 (download_plan.json)。
type DownloadPlan struct {
	TargetPackages []string      `json:"target_packages"`
	PackageManager string        `json:"package_manager"` // "apt" 或 "yum"
	Packages       []PackageInfo `json:"packages"`
}

// PackageManager 定义跨发行版的包管理器抽象接口。
// 所有针对 Debian 系或 RHEL 系的操作都必须实现该接口。
type PackageManager interface {
	// Name 返回包管理器的名称标识 (如 "apt", "yum")
	Name() string

	// Detect 检测当前系统是否适用该包管理器
	Detect() bool

	// GetMetadataURLs 解析本地源配置，返回需要下载的元数据 URL 和存储路径
	GetMetadataURLs() ([]MetaFile, error)

	// InjectMetaAndPlan 将下载好的元数据注入系统缓存，
	// 执行 dry-run 计算依赖，返回实际安装包的信息列表。
	// metaDir: 元数据文件解压后的目录路径
	// targetPackages: 用户想要安装的软件名列表
	InjectMetaAndPlan(metaDir string, targetPackages []string) ([]PackageInfo, error)

	// InjectPackagesAndInstall 将下载好的安装包注入缓存，并执行原生安装命令。
	// pkgDir: 安装包文件解压后的目录路径
	// targetPackages: 用户想要安装的软件名列表
	InjectPackagesAndInstall(pkgDir string, targetPackages []string) error
}
