package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	targetURL   string
	concurrency int
	delay       time.Duration
	maxRetries  int
	duration    int
)

func init() {
	flag.StringVar(&targetURL, "url", "", "目标 URL")
	flag.IntVar(&concurrency, "c", 10, "并发协程数")
	flag.DurationVar(&delay, "d", 300*time.Millisecond, "请求间隔")
	flag.IntVar(&maxRetries, "r", 3, "最大重试次数")
	flag.IntVar(&duration, "t", 0, "程序运行时间（秒），0 表示一直运行")
	flag.Parse()

	if targetURL == "" {
		fmt.Println("请提供目标 URL")
		os.Exit(1)
	}
}

func main() {
	var ctx context.Context
	var cancel context.CancelFunc

	if duration > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), time.Duration(duration)*time.Second)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	defer cancel()

	var wg sync.WaitGroup
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	sem := make(chan struct{}, concurrency)
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	var successCount, failureCount int64

	// 定期打印统计信息
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				fmt.Printf("成功请求: %d, 失败请求: %d\n", atomic.LoadInt64(&successCount), atomic.LoadInt64(&failureCount))
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-stop:
			fmt.Println("接收到结束信号，等待协程结束...")
			cancel()
			wg.Wait()
			fmt.Println("所有协程已结束，程序退出。")
			return
		case <-ctx.Done():
			if duration > 0 {
				fmt.Printf("达到指定运行时间 %d 秒，程序即将退出...\n", duration)
			}
			wg.Wait()
			fmt.Println("所有协程已结束，程序退出。")
			return
		default:
			wg.Add(1)
			go func() {
				defer wg.Done()

				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					return
				}

				for retries := 0; retries <= maxRetries; retries++ {
					select {
					case <-ctx.Done():
						return
					default:
						resp, err := client.Get(targetURL)
						if err != nil {
							if retries == maxRetries {
								fmt.Printf("达到最大重试次数，放弃请求: %s\n", err)
								atomic.AddInt64(&failureCount, 1)
								return
							}
							fmt.Printf("请求失败：%s，正在进行重试（第%d次）...\n", err, retries+1)
							time.Sleep(delay)
							continue
						}

						fmt.Printf("请求成功: %s\n", resp.Status)
						resp.Body.Close()
						atomic.AddInt64(&successCount, 1)
						break
					}
				}

				time.Sleep(delay)
			}()
		}
	}
}