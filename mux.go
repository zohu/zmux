package zmux

import (
	"io"
	"time"
)

const (
	defaultMaxStream          = 256
	defaultMaxStreamSize      = 256 * 1024
	defaultKeepAliveInterval  = 30 * time.Second
	defaultTimeoutWrite       = 10 * time.Second
	defaultTimeoutStreamOpen  = 75 * time.Second
	defaultTimeoutStreamClose = 5 * time.Minute
)

type Logger interface {
	Infof(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

type Config struct {
	MaxStream          int
	MaxStreamSize      uint32
	KeepAlive          *bool
	KeepAliveInterval  time.Duration
	TimeoutWrite       time.Duration
	TimeoutStreamOpen  time.Duration
	TimeoutStreamClose time.Duration
	LogWriter          Logger
}

func verifyConfig(conf *Config) *Config {
	if conf == nil {
		conf = &Config{}
	}
	if conf.KeepAlive == nil {
		conf.KeepAlive = Ptr(true)
	}
	if conf.MaxStream <= 0 {
		conf.MaxStream = defaultMaxStream
	}
	if conf.MaxStreamSize <= 0 {
		conf.MaxStreamSize = defaultMaxStreamSize
	}
	if conf.KeepAliveInterval == 0 {
		conf.KeepAliveInterval = defaultKeepAliveInterval
	}
	if conf.TimeoutWrite == 0 {
		conf.TimeoutWrite = defaultTimeoutWrite
	}
	if conf.TimeoutStreamOpen == 0 {
		conf.TimeoutStreamOpen = defaultTimeoutStreamOpen
	}
	if conf.TimeoutStreamClose == 0 {
		conf.TimeoutStreamClose = defaultTimeoutStreamClose
	}
	if conf.LogWriter == nil {
		conf.LogWriter = &defaultLogger{}
	}
	return conf
}

func Server(conn io.ReadWriteCloser, conf *Config) *Session {
	return newSession(verifyConfig(conf), conn, false)
}
func Client(conn io.ReadWriteCloser, conf *Config) *Session {
	return newSession(verifyConfig(conf), conn, true)
}
