package archive

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// CreateBundle 将 sourceDir 中的所有文件打包为 tar.gz 格式的 bundle 文件
func CreateBundle(bundlePath string, sourceDir string) error {
	// 确保目标文件的父目录存在
	if err := os.MkdirAll(filepath.Dir(bundlePath), 0755); err != nil {
		return fmt.Errorf("创建输出目录失败: %v", err)
	}

	outFile, err := os.Create(bundlePath)
	if err != nil {
		return fmt.Errorf("创建文件失败: %v", err)
	}
	defer outFile.Close()

	gw := gzip.NewWriter(outFile)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	err = filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// 获取相对路径
		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return fmt.Errorf("获取相对路径失败: %v", err)
		}
		
		// 忽略根目录本身
		if relPath == "." {
			return nil
		}
		
		// 统一使用正斜杠
		relPath = filepath.ToSlash(relPath)

		header, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			return fmt.Errorf("生成tar头失败: %v", err)
		}

		header.Name = relPath

		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("写入tar头失败: %v", err)
		}

		// 如果是普通文件，写入文件内容
		if info.Mode().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("打开源文件失败: %v", err)
			}
			defer file.Close()

			if _, err := io.Copy(tw, file); err != nil {
				return fmt.Errorf("写入文件内容失败: %v", err)
			}
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("打包过程失败: %v", err)
	}

	return nil
}

// ExtractBundle 解压 bundle 文件到目标目录
func ExtractBundle(bundlePath string, destDir string) error {
	inFile, err := os.Open(bundlePath)
	if err != nil {
		return fmt.Errorf("打开文件失败: %v", err)
	}
	defer inFile.Close()

	gr, err := gzip.NewReader(inFile)
	if err != nil {
		return fmt.Errorf("初始化gzip读取器失败: %v", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	// 确保目标目录存在，并获取绝对路径以防 zip slip 攻击
	destDirAbs, err := filepath.Abs(destDir)
	if err != nil {
		return fmt.Errorf("获取目标目录绝对路径失败: %v", err)
	}

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("读取tar头失败: %v", err)
		}

		target := filepath.Join(destDirAbs, filepath.FromSlash(header.Name))
		
		// 防止 Zip Slip 攻击
		if !strings.HasPrefix(target, destDirAbs+string(filepath.Separator)) && target != destDirAbs {
			return fmt.Errorf("发现非法路径，可能存在路径穿越攻击: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("创建目录失败: %v", err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("创建父目录失败: %v", err)
			}

			file, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("创建文件失败: %v", err)
			}

			if _, err := io.Copy(file, tr); err != nil {
				file.Close()
				return fmt.Errorf("解压文件内容失败: %v", err)
			}
			file.Close()
		default:
			// 忽略其他类型如符号链接等，或者可以根据需求实现
		}
	}

	return nil
}

// ListBundle 列出 bundle 中的文件清单
func ListBundle(bundlePath string) ([]string, error) {
	inFile, err := os.Open(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("打开文件失败: %v", err)
	}
	defer inFile.Close()

	gr, err := gzip.NewReader(inFile)
	if err != nil {
		return nil, fmt.Errorf("初始化gzip读取器失败: %v", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	var files []string

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("读取tar头失败: %v", err)
		}

		files = append(files, header.Name)
	}

	return files, nil
}
