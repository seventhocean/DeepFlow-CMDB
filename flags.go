package main

import (
	"flag"
	"strings"
	"time"
)

var cfg Flags

type Flags struct {
	dbFlags
	httpFlags
	LogPath  string
	LogDebug bool
}

type dbFlags struct {
	Database     string
	DBHost       string
	DBPort       uint
	UserName     string
	UserPassword string
}

// httpFlags HTTP 配置
type httpFlags struct {
	HTTPHost string
	HTTPPort uint
	Timeout  time.Duration
	Headers  map[string]string
	PageSize uint
}

// headerFlag 用于支持多次 -header "key:value" 参数
type headerFlag map[string]string

func (h headerFlag) String() string {
	if len(h) == 0 {
		return ""
	}
	pairs := make([]string, 0, len(h))
	for k, v := range h {
		pairs = append(pairs, k+":"+v)
	}
	return strings.Join(pairs, ",")
}

func (h headerFlag) Set(value string) error {
	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 {
		// 若没有冒号，则视为值为空
		h[strings.TrimSpace(value)] = ""
		return nil
	}
	h[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	return nil
}

func parseFlags() Flags {
	// 初始化 map，避免为 nil
	cfg.Headers = map[string]string{
		"Accept": "application/json",
	}

	// ---------- dbFlags ----------
	flag.StringVar(&cfg.Database,
		"database",
		"deepflow",
		"数据库名")
	flag.StringVar(&cfg.DBHost,
		"db-host",
		"mysql",
		"数据库主机地址")
	flag.UintVar(&cfg.DBPort,
		"db-port",
		30130,
		"数据库端口")
	flag.StringVar(&cfg.UserName,
		"db-user-name",
		"root",
		"数据库用户名")
	flag.StringVar(&cfg.UserPassword,
		"db-user-pswd",
		"",
		"数据库密码")

	// ---------- httpFlags ----------
	flag.StringVar(&cfg.HTTPHost,
		"http-host",
		"dlink-api.qa.pab.com.cn",
		"HTTP 服务主机地址")
	flag.UintVar(&cfg.HTTPPort,
		"http-port",
		0,
		"HTTP 服务端口，默认不指定")
	flag.UintVar(&cfg.PageSize,
		"page-size",
		100,
		"分页大小，默认 100")
	flag.DurationVar(&cfg.Timeout,
		"http-timeout",
		30*time.Second,
		"HTTP 请求超时时间，单位秒")
	flag.Var(headerFlag(cfg.Headers),
		"http-header",
		"HTTP 请求头，格式 key:value，可多次指定，如 -header 'Authorization: xxx'")

	flag.StringVar(&cfg.LogPath,
		"log-path",
		"",
		"日志文件路径，默认使用可执行文件同目录 sync-biz-service.log")
	flag.BoolVar(&cfg.LogDebug,
		"log-debug",
		false,
		"是否开启调试日志，默认关闭")

	flag.Parse()

	return cfg
}
