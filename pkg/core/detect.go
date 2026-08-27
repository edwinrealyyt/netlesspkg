package core

import "fmt"

// registeredManagers 保存所有注册的包管理器实现。
var registeredManagers []PackageManager

// RegisterManager 注册一个包管理器实现。
// 各包管理器实现应在其 init() 函数中调用此方法完成注册。
func RegisterManager(pm PackageManager) {
	registeredManagers = append(registeredManagers, pm)
}

// DetectManager 自动检测当前系统的包管理器。
// 按注册顺序依次检测，返回第一个匹配的包管理器。
func DetectManager() (PackageManager, error) {
	for _, pm := range registeredManagers {
		if pm.Detect() {
			return pm, nil
		}
	}
	return nil, fmt.Errorf("错误：未能识别当前系统的包管理器\n" +
		"排障提示：\n" +
		"  1. 请确认系统已安装 apt-get（Debian/Ubuntu）或 yum/dnf（CentOS/RHEL）\n" +
		"  2. 请确认 /etc/os-release 文件存在且内容正常\n" +
		"  3. 当前仅支持 Debian 系和 RHEL 系 Linux 发行版")
}

// GetManagerByName 根据名称查找已注册的包管理器。
// 用于从 JSON 配置文件中恢复包管理器类型。
func GetManagerByName(name string) (PackageManager, error) {
	for _, pm := range registeredManagers {
		if pm.Name() == name {
			return pm, nil
		}
	}
	return nil, fmt.Errorf("错误：未找到名为 %q 的包管理器实现", name)
}
