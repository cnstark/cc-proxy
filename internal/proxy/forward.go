package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/cnstark/cc-proxy/internal/config"
	"github.com/cnstark/cc-proxy/internal/usage"
)

// StreamingForwarder 流式转发器 — 逐 chunk flush SSE 响应
type StreamingForwarder struct{}

// NewStreamingForwarder 创建默认转发器
func NewStreamingForwarder() *StreamingForwarder {
	return &StreamingForwarder{}
}

// ResponseStartedError 表示失败发生在响应已开始向客户端输出之后
// （即已经 WriteHeader 或写了首字节）。此时不可再转移到下一个上游，
// 否则会把两段响应拼接，导致客户端收到截断/混乱的 JSON。
// handler 通过 errors.As 检测此类型以遵守「流式已开始后不转移」的不变量。
type ResponseStartedError struct {
	Err error
}

func (e *ResponseStartedError) Error() string {
	return fmt.Sprintf("响应已开始后的失败，不可转移: %v", e.Err)
}

func (e *ResponseStartedError) Unwrap() error { return e.Err }

// UpstreamError 表示上游返回了非 2xx 响应。
// Body 为上游错误响应体(完整),由调用方在打印时截断。
// 仅用于 5xx/429 可重试路径——4xx 透传后 return nil 不上抛。
type UpstreamError struct {
	StatusCode int
	Body       []byte
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("上游返回非2xx: status=%d", e.StatusCode)
}

// Forward 发起上游请求并流式透传响应。
// c 非 nil 时把响应流旁路 Tee 给 usage 收集器；c 为 nil 时零开销直传。
// 不变量：响应已开始后（WriteHeader/首字节后）的失败包装为 ResponseStartedError，
// handler 据此不转移；连接阶段失败返回普通 error，handler 转移到下一个 cfg。
// usage 语义：连接阶段失败（client.Do err）时 collector 未 Attach、不 Close → 不计数；
// 成功流结束时 Close 触发一次 Record（仅当扫到 usage）。
func (f *StreamingForwarder) Forward(cfg config.Upstream, body []byte, headers http.Header, w http.ResponseWriter, c *usage.Collector, log *slog.Logger) error {
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
		// 连接阶段失败：响应尚未开始，可安全转移。collector 未 Attach，不计数。
		return fmt.Errorf("上游连接失败: %w", err)
	}
	defer resp.Body.Close()

	// Debug: 记录上游响应状态码和关键响应头
	if log != nil {
		log.DebugContext(context.Background(), "upstream response received",
			"status_code", resp.StatusCode,
			"status", resp.Status,
			"content-type", resp.Header.Get("content-type"),
			"content_len", resp.Header.Get("content-length"),
		)
	}

	// 非 2xx 响应：判断是否可重试
	// 可重试（5xx、429）：返回错误，让 handler 故障转移到下一个上游
	// 不可重试（其他 4xx）：透传错误响应给客户端
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			if log != nil {
				log.DebugContext(context.Background(), "failed to read upstream error body",
					"error", readErr.Error(),
				)
			}
			return fmt.Errorf("读取上游错误响应体失败: %w", readErr)
		}
		if log != nil {
			log.DebugContext(context.Background(), "upstream error response body",
				"body_len", len(errBody),
				"body_head", truncStr(string(errBody), 1024),
				"body_tail", truncTail(string(errBody), 256),
			)
		}

		// 可重试：5xx 或 429，不向客户端写任何响应，直接返回错误让 handler 转移
		if resp.StatusCode >= 500 || resp.StatusCode == 429 {
			return &UpstreamError{StatusCode: resp.StatusCode, Body: errBody}
		}

		// 不可重试：其他 4xx，直接透传响应
		// 因 return nil 不走 handler 失败分支,在此就地 INFO 打印详情(连接阶段失败不涉及)
		if log != nil {
			log.InfoContext(context.Background(), "upstream error response (non-retryable)",
				"upstream", cfg.Name,
				"status_code", resp.StatusCode,
				"body_head", truncStr(string(errBody), 1024),
			)
		}
		// 透传响应头
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		if _, writeErr := w.Write(errBody); writeErr != nil {
			return &ResponseStartedError{Err: fmt.Errorf("向客户端写入错误响应失败: %w", writeErr)}
		}
		// usage 收集：错误响应也可能包含 usage 信息（如 Anthropic 格式错误）
		if c != nil {
			c.Attach(resp.Header.Get("content-type"))
			c.Feed(errBody)
			c.Close()
		}
		return nil
	}

	// 2xx 响应：流式透传
	// collector 按响应 content-type 选解析模式。注册在连接成功之后：
	// 连接失败路径不会走到这里，Close 不会被调用 → 不计数。
	if c != nil {
		c.Attach(resp.Header.Get("content-type"))
		defer c.Close() // 流结束（含中途 EOF）触发 Record；已见部分照常 commit
	}

	// 透传响应头
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}

	// 透传状态码 —— 此刻起响应已开始，后续任何失败都不可转移到下一个上游
	w.WriteHeader(resp.StatusCode)

	// 逐 chunk 流式转发
	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				// 响应已开始，不可转移
				return &ResponseStartedError{Err: fmt.Errorf("向客户端写入失败: %w", writeErr)}
			}
			if canFlush {
				flusher.Flush()
			}
			// 旁路喂给 usage 收集器（在写给客户端之后，不增加客户端延迟）
			if c != nil {
				c.Feed(buf[:n])
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return &ResponseStartedError{Err: fmt.Errorf("读取上游响应失败: %w", readErr)}
		}
	}
	return nil
}
