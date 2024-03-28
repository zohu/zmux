package zmux

import (
	"fmt"
	"net"
)

type lrAddr interface {
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
}

type zmuxAddr struct {
	Addr string
}

func (*zmuxAddr) Network() string {
	return "zmux"
}

func (z *zmuxAddr) String() string {
	return fmt.Sprintf("zmux:%s", z.Addr)
}
