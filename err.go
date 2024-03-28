package zmux

import "fmt"

type NetError struct {
	err       error
	timeout   bool
	temporary bool
}

func (e *NetError) Error() string {
	return e.err.Error()
}

func (e *NetError) Timeout() bool {
	return e.timeout
}

func (e *NetError) Temporary() bool {
	return e.temporary
}

var (
	ErrStreamsExhausted = fmt.Errorf("streams exhausted")
	ErrRemoteGoAway     = fmt.Errorf("remote end is not accepting connections")
	ErrKeepAliveTimeout = fmt.Errorf("keepalive timeout")
	ErrDuplicateStream  = fmt.Errorf("duplicate stream initiated")
	ErrRecvSizeExceeded = fmt.Errorf("recv size exceeded")
	ErrUnexpectedFlag   = fmt.Errorf("unexpected flag")
	ErrInvalidVersion   = fmt.Errorf("invalid protocol version")
	ErrInvalidMsgType   = fmt.Errorf("invalid msg type")
	ErrStreamClosed     = fmt.Errorf("stream closed")
	ErrSessionShutdown  = fmt.Errorf("session shutdown")
	ErrConnectionReset  = fmt.Errorf("connection reset")
	ErrTimeout          = &NetError{
		err:     fmt.Errorf("i/o deadline reached"),
		timeout: true,
	}
)
