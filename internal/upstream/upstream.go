package upstream

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/cnstark/cc-proxy/internal/config"
	"github.com/cnstark/cc-proxy/internal/usage"
)

// Client 上游 HTTP 客户端
type Client struct {
	httpClient *http.Client
}

// NewClient 创建上游客户端
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			},
		},
	}
}

// Forward 向上游转发请求，返回响应（legacy 非流式实现，usage 不接入）。
func (c *Client) Forward(cfg config.Upstream, body []byte, headers http.Header, w http.ResponseWriter, _ *usage.Collector, _ *slog.Logger) error {
	req, err := http.NewRequest("POST", cfg.URL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("创建上游请求失败: %w", err)
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("x-api-key", cfg.APIKey)

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("上游请求失败: %w", err)
	}
	defer resp.Body.Close()

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
	return nil
}

// HealthCheck 检查上游是否可达（HEAD /）
func (c *Client) HealthCheck(cfg config.Upstream) bool {
	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Head(cfg.URL + "/")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}
