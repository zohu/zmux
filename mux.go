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
		KeepAlive:          true,
		KeepAliveInterval:  defaultKeepAliveInterval,
		TimeoutWrite:       defaultTimeoutWrite,
		TimeoutStreamOpen:  defaultTimeoutStreamOpen,
		TimeoutStreamClose: defaultTimeoutStreamClose,
		LogWriter:          &defaultLogger{},
	}
}

func Server(conn io.ReadWriteCloser, conf *Config) *Session {
	return newSession(conf, conn, false)
}

func Client(conn io.ReadWriteCloser, conf *Config) *Session {
	return newSession(conf, conn, true)
}
