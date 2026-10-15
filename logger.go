package main

import (
	"log"
	"os"
	"path/filepath"
)

var logger *log.Logger

// logFile 保存文件句柄，退出前关闭，确保日志落盘
var logFile *os.File

func initLogger(logPath string) error {
	if logPath == "" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		logPath = filepath.Join(filepath.Dir(exe), "sync-biz-service.log")
	}

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	logFile = f

	logger = log.New(f, "[app] ", log.LstdFlags|log.Lshortfile)
	return nil
}

// closeLogger 确保退出前日志落盘
func closeLogger() {
	if logFile != nil {
		_ = logFile.Sync()
		_ = logFile.Close()
	}
}
