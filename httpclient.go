package main

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/bitly/go-simplejson"
)

var httpclient *HTTPClient

func initHTTPClient() {
	httpclient = &HTTPClient{cfg: cfg.httpFlags}
}

type HTTPClient struct {
	cfg httpFlags
}

// Get 执行 HTTP 请求并返回 simplejson
func (h *HTTPClient) Get(url string) (*simplejson.Json, error) {
	// 设置默认超时
	timeout := h.cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	// 创建请求
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("new request failed: %w", err)
	}

	// 设置 headers
	for k, v := range h.cfg.Headers {
		req.Header.Set(k, v)
	}

	// 执行请求
	client := &http.Client{
		Timeout: timeout,
	}
	dump, err := httputil.DumpRequestOut(req, false)
	if err != nil {
		logger.Printf("dump failed: %v", err)
	} else {
		logger.Printf("request dump:\n%s", dump)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request failed: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应
	var body io.Reader = resp.Body
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		reader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("open gzip response failed: %w", err)
		}
		defer reader.Close()
		body = reader
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read body failed: %w", err)
	}

	// 解析 JSON
	js, err := simplejson.NewJson(data)
	if err != nil {
		return nil, fmt.Errorf("parse json failed: %w, body=%s", err, string(data))
	}

	// 非 2xx 状态码也返回错误（保留响应体在 error 里）
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return js, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(data))
	}

	return js, nil
}
