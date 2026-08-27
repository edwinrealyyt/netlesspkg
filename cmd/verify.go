package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"netlesspkg/pkg/archive"
	"netlesspkg/pkg/core"
	"os"
	"path/filepath"
)

func runVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	inFile := fs.String("i", "", "要校验的 bundle 文件路径（必填）")
	planFile := fs.String("m", "", "对应的 download_plan.json（可选）")
	
	if err := fs.Parse(args); err != nil {
		fmt.Printf("参数解析错误: %v\n", err)
		os.Exit(1)
	}

	if *inFile == "" {
		fmt.Println("必须指定输入 bundle 路径 (-i)")
		fs.Usage()
		os.Exit(1)
	}

	tmpDir, err := os.MkdirTemp("", "netlesspkg-verify-*")
	if err != nil {
		fmt.Printf("创建临时目录失败: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	if err := archive.ExtractBundle(*inFile, tmpDir); err != nil {
		fmt.Printf("解压 bundle 失败: %v\n", err)
		os.Exit(1)
	}

	expectedHashes := make(map[string]string)
	if *planFile != "" {
		data, err := os.ReadFile(*planFile)
		if err == nil {
			var plan core.DownloadPlan
			if json.Unmarshal(data, &plan) == nil {
				for _, p := range plan.Packages {
					if p.SHA256 != "" {
						expectedHashes[p.Filename] = p.SHA256
					}
				}
			}
		} else {
			fmt.Printf("读取 download_plan.json 失败: %v\n", err)
		}
	}

	files, err := os.ReadDir(tmpDir)
	if err != nil {
		fmt.Printf("读取临时目录失败: %v\n", err)
		os.Exit(1)
	}

	passCount, failCount, skipCount := 0, 0, 0
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		path := filepath.Join(tmpDir, f.Name())
		expectedHash, ok := expectedHashes[f.Name()]
		if !ok {
			skipCount++
			continue
		}
		
		actualHash, err := calcSHA256(path)
		if err != nil {
			fmt.Printf("计算 %s 哈希失败: %v\n", f.Name(), err)
			failCount++
			continue
		}
		
		if actualHash == expectedHash {
			passCount++
		} else {
			fmt.Printf("校验失败: %s (预期 %s, 实际 %s)\n", f.Name(), expectedHash, actualHash)
			failCount++
		}
	}

	fmt.Printf("校验结果: 通过 %d, 失败 %d, 跳过 %d\n", passCount, failCount, skipCount)
	if failCount > 0 {
		os.Exit(1)
	}
}

func calcSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
