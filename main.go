package main

import (
	"context"
	"flag"
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

var (
	targetURL     string
	concurrency   int
	outputDir     string
	downloadCount int
	chunkSize     int64 = 1024 * 1024 // 1MB
)

func init() {
	flag.StringVar(&targetURL, "url", "", "目标 URL")
	flag.IntVar(&concurrency, "c", 5, "每个文件的并发下载协程数")
	flag.StringVar(&outputDir, "o", ".", "输出目录")
	flag.IntVar(&downloadCount, "n", 20, "下载次数")
	flag.Parse()

	if targetURL == "" {
		fmt.Println("请提供目标 URL")
		os.Exit(1)
	}
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	var wg sync.WaitGroup
	var successCount, failureCount int32

	for i := 0; i < downloadCount; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			err := downloadFileWithChunks(ctx, client, targetURL, outputDir, index, concurrency)
			if err != nil {
				fmt.Printf("下载文件 %d 失败: %s\n", index, err)
				atomic.AddInt32(&failureCount, 1)
				return
			}
			atomic.AddInt32(&successCount, 1)
			fmt.Printf("文件 %d 下载完成\n", index)
		}(i)
	}

	wg.Wait()

	fmt.Printf("下载完成。成功: %d, 失败: %d\n", atomic.LoadInt32(&successCount), atomic.LoadInt32(&failureCount))
}

func downloadFileWithChunks(ctx context.Context, client *http.Client, url, outputDir string, index, concurrency int) error {
	// 获取文件大小
	size, err := getFileSize(client, url)
	if err != nil {
		return fmt.Errorf("获取文件大小失败: %w", err)
	}

	fileName := fmt.Sprintf("%s_%d%s", filepath.Base(url[:len(url)-len(filepath.Ext(url))]), index, filepath.Ext(url))
	filePath := filepath.Join(outputDir, fileName)

	// 创建输出文件
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("创建文件失败: %w", err)
	}
	defer file.Close()

	// 计算分片
	chunks := make([][2]int64, 0)
	for start := int64(0); start < size; start += chunkSize {
		end := start + chunkSize - 1
		if end > size-1 {
			end = size - 1
		}
		chunks = append(chunks, [2]int64{start, end})
	}

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, concurrency)
	errChan := make(chan error, len(chunks))

	for i, chunk := range chunks {
		wg.Add(1)
		go func(i int, chunk [2]int64) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			err := downloadChunk(ctx, client, url, file, chunk[0], chunk[1])
			if err != nil {
				errChan <- fmt.Errorf("下载分片 %d 失败: %w", i, err)
				return
			}
			fmt.Printf("文件 %d 的分片 %d 下载完成\n", index, i)
		}(i, chunk)
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		return err
	}

	return nil
}

func getFileSize(client *http.Client, url string) (int64, error) {
	resp, err := client.Head(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("服务器返回非200状态码: %s", resp.Status)
	}

	size, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("无法获取文件大小: %w", err)
	}

	return size, nil
}

func downloadChunk(ctx context.Context, client *http.Client, url string, file *os.File, start, end int64) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("服务器返回非206状态码: %s", resp.Status)
	}

	_, err = file.Seek(start, io.SeekStart)
	if err != nil {
		return err
	}

	_, err = io.Copy(file, resp.Body)
	return err
}
