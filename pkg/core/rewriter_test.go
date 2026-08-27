package core

import "testing"

func TestURLRewriter(t *testing.T) {
	// 1. 测试内置阿里云内网源自动映射
	rwAuto, err := NewURLRewriter(nil, true)
	if err != nil {
		t.Fatalf("创建 rewriter 失败: %v", err)
	}

	testURL := "http://mirrors.cloud.aliyuncs.com/alinux/3/powertools/aarch64/repodata/repomd.xml"
	expectedURL := "http://mirrors.aliyun.com/alinux/3/powertools/aarch64/repodata/repomd.xml"

	rewritten, replaced, _ := rwAuto.RewriteURL(testURL)
	if !replaced || rewritten != expectedURL {
		t.Errorf("内置自动映射失败, 期望: %s, 实际: %s (replaced=%v)", expectedURL, rewritten, replaced)
	}

	// 2. 测试禁用自动映射
	rwNoAuto, _ := NewURLRewriter(nil, false)
	rewrittenNoAuto, replacedNoAuto, _ := rwNoAuto.RewriteURL(testURL)
	if replacedNoAuto || rewrittenNoAuto != testURL {
		t.Errorf("禁用自动映射失败, 不应替换")
	}

	// 3. 测试自定义规则
	customRules := []string{"http://192.168.1.100/=https://mirrors.tuna.tsinghua.edu.cn/"}
	rwCustom, err := NewURLRewriter(customRules, false)
	if err != nil {
		t.Fatalf("创建自定义 rewriter 失败: %v", err)
	}

	internalURL := "http://192.168.1.100/ubuntu/dists/jammy/InRelease"
	expectedCustom := "https://mirrors.tuna.tsinghua.edu.cn/ubuntu/dists/jammy/InRelease"
	rewrittenCustom, replacedCustom, _ := rwCustom.RewriteURL(internalURL)
	if !replacedCustom || rewrittenCustom != expectedCustom {
		t.Errorf("自定义映射失败, 期望: %s, 实际: %s", expectedCustom, rewrittenCustom)
	}
}
