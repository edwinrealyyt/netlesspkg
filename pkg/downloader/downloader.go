package downloader

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// DownloadTask 表示一个下载任务
type DownloadTask struct {
	URL      string // 下载地址
	SavePath string // 本地保存路径（绝对路径）
	SHA256   string // 可选，下载后校验
	Size     int64  // 可选，预期文件大小
}

// DownloadResult 表示下载结果
type DownloadResult struct {
	Task  DownloadTask
	Error error
}

// Options 下载器配置
type Options struct {
	Concurrency  int  // 并发数，默认 4
	RetryCount   int  // 重试次数，默认 3
	EnableResume bool // 是否启用断点续传，默认 true
	ShowProgress bool // 是否显示进度，默认 true
}

type progressWriter struct {
	total       int64
	current     int64
	task        *DownloadTask
	taskIndex   int
	totalTasks  int
	mu          *sync.Mutex
	show        bool
	lastShow    time.Time
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	atomic.AddInt64(&pw.current, int64(n))
	
	if pw.show {
		now := time.Now()
		if now.Sub(pw.lastShow) > 100*time.Millisecond {
			pw.mu.Lock()
			cur := atomic.LoadInt64(&pw.current)
			percent := float64(0)
			if pw.total > 0 {
				percent = float64(cur) * 100 / float64(pw.total)
			}
			filename := filepath.Base(pw.task.SavePath)
			fmt.Printf("\r[%d/%d] 下载中: %s %.1f MB / %.1f MB (%.0f%%)", 
				pw.taskIndex, pw.totalTasks, filename, 
				float64(cur)/1024/1024, float64(pw.total)/1024/1024, percent)
			pw.lastShow = now
			pw.mu.Unlock()
		}
	}
	return n, nil
}

func downloadSingle(task DownloadTask, opts Options, index, totalTasks int, printMu *sync.Mutex) error {
	client := &http.Client{
		Timeout: 30 * time.Second, // 此处只设连接超时等可以通过Transport设置，为简单保留
	}
	
	// Custom transport for connection timeout but no read/write timeout (so large files can download)
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	client.Transport = transport
	client.Timeout = 0 // 无总体超时

	retryCount := opts.RetryCount
	if retryCount <= 0 {
		retryCount = 3
	}

	var err error
	for i := 0; i <= retryCount; i++ {
		if i > 0 {
			time.Sleep(time.Duration(1<<uint(i-1)) * time.Second)
		}
		err = doDownload(client, task, opts, index, totalTasks, printMu)
		if err == nil {
			break
		}
	}
	
	if err != nil {
		return err
	}

	// 校验 SHA256
	if task.SHA256 != "" {
		hash, err := calcSHA256(task.SavePath)
		if err != nil {
			return fmt.Errorf("计算哈希失败: %v", err)
		}
		if hash != task.SHA256 {
			return fmt.Errorf("哈希校验失败, 期望: %s, 实际: %s", task.SHA256, hash)
		}
	}

	return nil
}

func doDownload(client *http.Client, task DownloadTask, opts Options, index, totalTasks int, printMu *sync.Mutex) error {
	// 确保目录存在
	dir := filepath.Dir(task.SavePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %v", err)
	}

	partPath := task.SavePath + ".part"
	var offset int64 = 0
	var out *os.File
	var err error

	if opts.EnableResume {
		if info, err := os.Stat(partPath); err == nil && info.Size() > 0 {
			offset = info.Size()
		}
	} else {
		os.Remove(partPath)
	}

	req, err := http.NewRequest("GET", task.URL, nil)
	if err != nil {
		return fmt.Errorf("创建请求失败: %v", err)
	}

	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("HTTP状态码错误: %d", resp.StatusCode)
	}

	isPartial := resp.StatusCode == http.StatusPartialContent

	if offset > 0 && !isPartial {
		// 服务器不支持断点续传，重头开始
		offset = 0
		out, err = os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	} else {
		flags := os.O_CREATE | os.O_WRONLY
		if isPartial {
			flags |= os.O_APPEND
		} else {
			flags |= os.O_TRUNC
		}
		out, err = os.OpenFile(partPath, flags, 0644)
	}
	if err != nil {
		return fmt.Errorf("打开文件失败: %v", err)
	}
	defer out.Close()

	totalSize := task.Size
	if totalSize == 0 {
		contentLength := resp.Header.Get("Content-Length")
		if contentLength != "" {
			if cl, err := strconv.ParseInt(contentLength, 10, 64); err == nil {
				if isPartial {
					totalSize = offset + cl
				} else {
					totalSize = cl
				}
			}
		}
	}

	pw := &progressWriter{
		total:      totalSize,
		current:    offset,
		task:       &task,
		taskIndex:  index,
		totalTasks: totalTasks,
		mu:         printMu,
		show:       opts.ShowProgress,
		lastShow:   time.Now(),
	}

	_, err = io.Copy(out, io.TeeReader(resp.Body, pw))
	if err != nil {
		return fmt.Errorf("下载数据失败: %v", err)
	}

	if opts.ShowProgress {
		printMu.Lock()
		fmt.Printf("\r[%d/%d] 下载完成: %s                                     \n", index, totalTasks, filepath.Base(task.SavePath))
		printMu.Unlock()
	}

	out.Close()
	return os.Rename(partPath, task.SavePath)
}

func calcSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// Download 并发下载一组任务
func Download(tasks []DownloadTask, opts Options) []DownloadResult {
	if opts.Concurrency <= 0 {
		opts.Concurrency = 4
	}
	if opts.RetryCount <= 0 {
		opts.RetryCount = 3
	}

	results := make([]DownloadResult, len(tasks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, opts.Concurrency)
	printMu := &sync.Mutex{}

	startTime := time.Now()
	var totalBytes int64

	for i, task := range tasks {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, t DownloadTask) {
			defer wg.Done()
			defer func() { <-sem }()
			
			err := downloadSingle(t, opts, idx+1, len(tasks), printMu)
			results[idx] = DownloadResult{
				Task:  t,
				Error: err,
			}
			if err == nil {
				if info, statErr := os.Stat(t.SavePath); statErr == nil {
					atomic.AddInt64(&totalBytes, info.Size())
				}
			}
		}(i, task)
	}

	wg.Wait()

	if opts.ShowProgress {
		duration := time.Since(startTime)
		printMu.Lock()
		fmt.Printf("\r下载完成: %d 个文件, 总计 %.1f MB, 耗时 %.0fs\n", 
			len(tasks), float64(totalBytes)/1024/1024, duration.Seconds())
		printMu.Unlock()
	}

	return results
}
