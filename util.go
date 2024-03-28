package zmux

import (
	"fmt"
	"sync"
	"time"
)

var timerPool = &sync.Pool{
	New: func() interface{} {
		timer := time.NewTimer(time.Hour * 1e6)
		timer.Stop()
		return timer
	},
}

func sendNotify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func sendErr(ch chan error, err error) {
	if ch == nil {
		return
	}
	select {
	case ch <- err:
	default:
	}
}

type defaultLogger struct{}

func (defaultLogger) Infof(format string, v ...interface{}) {
	fmt.Println(fmt.Sprintf("=> "+format, v...))
}
func (defaultLogger) Errorf(format string, v ...interface{}) {
	fmt.Println(fmt.Sprintf(format, v...))
}
