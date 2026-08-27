package core

import (
	"fmt"
	"strings"
)

// DefaultDomainMappings 内置的常见云厂商内网源 -> 公网源映射表
var DefaultDomainMappings = map[string]string{
	"mirrors.cloud.aliyuncs.com": "mirrors.aliyun.com",
	"mirrors.tencentyun.com":     "mirrors.tencent.com",
	"mirrors.myhuaweicloud.com":  "repo.huaweicloud.com",
}

// URLRewriter 用于在下载前将内网/私有源 URL 重写为外网可访问的镜像 URL
type URLRewriter struct {
	customRules map[string]string // 用户自定义的旧字符串 -> 新字符串
	enableAuto  bool              // 是否启用内置云厂商内网源自动映射
}

// NewURLRewriter 创建一个 URL 重写器
// replaceFlags: 例如 ["mirrors.cloud.aliyuncs.com=mirrors.aliyun.com", "http://int.repo/=http://ext.repo/"]
func NewURLRewriter(replaceFlags []string, enableAuto bool) (*URLRewriter, error) {
	rw := &URLRewriter{
		customRules: make(map[string]string),
		enableAuto:  enableAuto,
	}

	for _, flag := range replaceFlags {
		flag = strings.TrimSpace(flag)
		if flag == "" {
			continue
		}
		parts := strings.SplitN(flag, "=", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("无效的替换规则 %q，格式必须为 <旧字符串>=<新字符串>", flag)
		}
		rw.customRules[parts[0]] = parts[1]
	}

	return rw, nil
}

// RewriteURL 根据规则重写 URL
// 如果发生了替换，返回替换后的 URL 和是否被替换
func (rw *URLRewriter) RewriteURL(rawURL string) (string, bool, string) {
	// 1. 优先应用用户自定义的精确规则
	for oldPattern, newPattern := range rw.customRules {
		if strings.Contains(rawURL, oldPattern) {
			newURL := strings.Replace(rawURL, oldPattern, newPattern, 1)
			return newURL, true, fmt.Sprintf("%s -> %s (自定义规则)", oldPattern, newPattern)
		}
	}

	// 2. 如果开启了自动映射，检查内置云厂商规则
	if rw.enableAuto {
		for internalDomain, publicDomain := range DefaultDomainMappings {
			if strings.Contains(rawURL, internalDomain) {
				newURL := strings.Replace(rawURL, internalDomain, publicDomain, 1)
				return newURL, true, fmt.Sprintf("%s -> %s (内置云源自动映射)", internalDomain, publicDomain)
			}
		}
	}

	return rawURL, false, ""
}

// CheckInternalURLError 检查失败的 URL 是否可能是内网专用源导致的，若是则生成排障提示
func CheckInternalURLError(urlStr string) string {
	for internalDomain, publicDomain := range DefaultDomainMappings {
		if strings.Contains(urlStr, internalDomain) {
			return fmt.Sprintf("排障建议：检测到该 URL 使用了云厂商内网专用源 [%s]，外网环境无法直接解析/访问。\n"+
				"系统默认会自动尝试映射为公网源 [%s]。如需手动指定，请添加参数：\n"+
				"  --replace %s=%s", internalDomain, publicDomain, internalDomain, publicDomain)
		}
	}

	if strings.Contains(urlStr, "10.") || strings.Contains(urlStr, "192.168.") ||
		strings.Contains(urlStr, "172.16.") || strings.Contains(urlStr, "100.100.") {
		return "排障建议：检测到该 URL 指向内网私有 IP 地址，外网机器无法直接下载。\n" +
			"请使用 --replace 参数将内网地址替换为对应的公网镜像站 URL。\n" +
			"例如: --replace http://192.168.1.100/=http://mirrors.aliyun.com/"
	}

	return ""
}
