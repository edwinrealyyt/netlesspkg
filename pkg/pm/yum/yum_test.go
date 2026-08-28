package yum

import (
	"testing"
)

func TestRestoreRemoteURLs(t *testing.T) {
	urls := map[string]bool{
		"file:///tmp/netlesspkg-plan-2733915488/alinux3-plus/Packages/xrdp-0.9.23.1-1.al8.aarch64.rpm": true,
		"file:///tmp/netlesspkg-plan-2733915488/epel/Packages/x/xrdp-0.10.6.1-3.el8.aarch64.rpm":        true,
		"http://mirrors.aliyun.com/epel/8/Everything/x86_64/Packages/n/nginx.rpm":                       true,
	}

	repoBaseURLs := map[string]string{
		"alinux3-plus": "http://mirrors.cloud.aliyuncs.com/alinux/3/plus/aarch64/",
		"epel":         "http://mirrors.cloud.aliyuncs.com/epel/8/Everything/aarch64/",
	}

	packages := restoreRemoteURLs(urls, repoBaseURLs, "/tmp/netlesspkg-plan-2733915488")
	if len(packages) != 3 {
		t.Fatalf("期望 3 个包，实际得到 %d 个", len(packages))
	}

	urlMap := make(map[string]string)
	for _, p := range packages {
		urlMap[p.Filename] = p.URL
	}

	expectedAlinux := "http://mirrors.cloud.aliyuncs.com/alinux/3/plus/aarch64/Packages/xrdp-0.9.23.1-1.al8.aarch64.rpm"
	if urlMap["xrdp-0.9.23.1-1.al8.aarch64.rpm"] != expectedAlinux {
		t.Errorf("alinux3-plus URL 还原失败, 期望: %s, 实际: %s",
			expectedAlinux, urlMap["xrdp-0.9.23.1-1.al8.aarch64.rpm"])
	}

	expectedEpel := "http://mirrors.cloud.aliyuncs.com/epel/8/Everything/aarch64/Packages/x/xrdp-0.10.6.1-3.el8.aarch64.rpm"
	if urlMap["xrdp-0.10.6.1-3.el8.aarch64.rpm"] != expectedEpel {
		t.Errorf("epel URL 还原失败, 期望: %s, 实际: %s",
			expectedEpel, urlMap["xrdp-0.10.6.1-3.el8.aarch64.rpm"])
	}

	if urlMap["nginx.rpm"] != "http://mirrors.aliyun.com/epel/8/Everything/x86_64/Packages/n/nginx.rpm" {
		t.Errorf("http URL 应该保持原样")
	}
}
