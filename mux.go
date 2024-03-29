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
	KeepAlive          bool
	KeepAliveInterval  time.Duration
	TimeoutWrite       time.Duration
	TimeoutStreamOpen  time.Duration
	TimeoutStreamClose time.Duration
	LogWriter          Logger
}

func DefaultConfig() *Config {
	return &Config{
		MaxStream:          defaultMaxStream,
		MaxStreamSize:      defaultMaxStreamSize,
		KeepAliveInterval:  defaultKeepAliveInterval,
		TimeoutWrite:       defaultTimeoutWrite,
		TimeoutStreamOpen:  defaultTimeoutStreamOpen,
		TimeoutStreamClose: defaultTimeoutStreamClose,
		LogWriter:          &defaultLogger{},
	}
}
func verifyConfig(config *Config) *Config {
	if config.MaxStream <= 0 {
		config.MaxStream = defaultMaxStream
	}
	if config.MaxStreamSize == 0 {
		config.MaxStreamSize = defaultMaxStreamSize
	}
	if config.KeepAliveInterval == 0 {
		config.KeepAliveInterval = defaultKeepAliveInterval
	}
	if config.TimeoutWrite == 0 {
		config.TimeoutWrite = defaultTimeoutWrite
	}
	if config.TimeoutStreamOpen == 0 {
		config.TimeoutStreamOpen = defaultTimeoutStreamOpen
	}
	if config.TimeoutStreamClose == 0 {
		config.TimeoutStreamClose = defaultTimeoutStreamClose
	}
	if config.LogWriter == nil {
		config.LogWriter = &defaultLogger{}
	}
	return config
}

func Server(conn io.ReadWriteCloser, conf *Config) *Session {
	return newSession(verifyConfig(conf), conn, false)
}
func Client(conn io.ReadWriteCloser, conf *Config) *Session {
	return newSession(verifyConfig(conf), conn, true)
}
