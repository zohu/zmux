package zmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"
)

type pipeConn struct {
	reader       *io.PipeReader
	writer       *io.PipeWriter
	writeBlocker sync.Mutex
}

func (p *pipeConn) Read(b []byte) (int, error) {
	return p.reader.Read(b)
}
func (p *pipeConn) Write(b []byte) (int, error) {
	p.writeBlocker.Lock()
	defer p.writeBlocker.Unlock()
	return p.writer.Write(b)
}
func (p *pipeConn) Close() error {
	if err := p.reader.Close(); err != nil {
		return err
	}
	return p.writer.Close()
}

func _conf() *Config {
	return &Config{
		MaxStream:         64,
		KeepAliveInterval: 100 * time.Millisecond,
		TimeoutWrite:      250 * time.Millisecond,
	}
}
func _conn() (io.ReadWriteCloser, io.ReadWriteCloser) {
	read1, write1 := io.Pipe()
	read2, write2 := io.Pipe()
	conn1 := &pipeConn{reader: read1, writer: write2}
	conn2 := &pipeConn{reader: read2, writer: write1}
	return conn1, conn2
}
func _cs(conf *Config) (*Session, *Session) {
	if conf == nil {
		conf = _conf()
	}
	conn1, conn2 := _conn()
	client := Client(conn1, conf)
	server := Server(conn2, conf)
	return client, server
}

func TestPing(t *testing.T) {
	client, server := _cs(nil)
	defer client.Close()
	defer server.Close()
	addr := client.Addr()
	addr.Network()
	addr.String()
	rtt, err := client.Ping()
	if err != nil {
		t.Fatalf("ping failed: %v", err)
	}
	if rtt == 0 {
		t.Fatalf("ping failed: rtt is zero")
	}
	rtt, err = server.Ping()
	if err != nil {
		t.Fatalf("ping failed: %v", err)
	}
	if rtt == 0 {
		t.Fatalf("ping failed: rtt is zero")
	}
}
func TestPingTimeout(t *testing.T) {
	client, server := _cs(nil)
	defer client.Close()
	defer server.Close()
	clientConn := client.conn.(*pipeConn)
	clientConn.writeBlocker.Lock()
	errCh := make(chan error, 1)
	go func() {
		_, err := server.Ping()
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("err: %v", err)
		}
	case <-time.After(client.conf.TimeoutWrite * 2):
		t.Fatalf("failed to timeout within expected %v", client.conf.TimeoutWrite)
	}

	clientConn.writeBlocker.Unlock()

	go func() {
		_, err := server.Ping()
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("err: %v", err)
		}
	case <-time.After(client.conf.TimeoutWrite):
		t.Fatalf("timeout")
	}
}
func TestCloseBeforeAck(t *testing.T) {
	client, server := _cs(nil)
	defer client.Close()
	defer server.Close()
	for i := 0; i < 8; i++ {
		s, err := client.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	for i := 0; i < 8; i++ {
		s, err := server.AcceptStream()
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s, err := client.OpenStream()
		if err != nil {
			t.Error(err)
			return
		}
		s.Close()
	}()
	select {
	case <-done:
	case <-time.After(time.Second * 5):
		t.Fatal("timed out trying to open stream")
	}
}
func TestAccept(t *testing.T) {
	client, server := _cs(nil)
	defer client.Close()
	defer server.Close()

	if client.NumStreams() != 0 {
		t.Fatalf("bad")
	}
	if server.NumStreams() != 0 {
		t.Fatalf("bad")
	}

	wg := &sync.WaitGroup{}
	wg.Add(4)

	go func() {
		defer wg.Done()
		stream, err := server.AcceptStream()
		if err != nil {
			t.Errorf("err: %v", err)
			return
		}
		if id := stream.ID(); id != 1 {
			t.Errorf("bad: %v", id)
			return
		}
		if err := stream.Close(); err != nil {
			t.Errorf("err: %v", err)
			return
		}
	}()

	go func() {
		defer wg.Done()
		stream, err := client.AcceptStream()
		if err != nil {
			t.Errorf("err: %v", err)
			return
		}
		if id := stream.ID(); id != 2 {
			t.Errorf("bad: %v", id)
			return
		}
		if err := stream.Close(); err != nil {
			t.Errorf("err: %v", err)
			return
		}
	}()

	go func() {
		defer wg.Done()
		stream, err := server.OpenStream()
		if err != nil {
			t.Errorf("err: %v", err)
			return
		}
		if id := stream.ID(); id != 2 {
			t.Errorf("bad: %v", id)
			return
		}
		if err := stream.Close(); err != nil {
			t.Errorf("err: %v", err)
			return
		}
	}()

	go func() {
		defer wg.Done()
		stream, err := client.OpenStream()
		if err != nil {
			t.Errorf("err: %v", err)
			return
		}
		if id := stream.ID(); id != 1 {
			t.Errorf("bad: %v", id)
			return
		}
		if err := stream.Close(); err != nil {
			t.Errorf("err: %v", err)
			return
		}
	}()

	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()

	select {
	case <-doneCh:
	case <-time.After(time.Second):
		panic("timeout")
	}
}
func TestOpenStreamTimeout(t *testing.T) {
	conf := _conf()
	conf.TimeoutStreamOpen = 25 * time.Millisecond
	client, server := _cs(conf)
	defer client.Close()
	defer server.Close()

	s, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(conf.TimeoutStreamOpen * 5)

	if s.state != stateClosed {
		t.Fatalf("stream should have been closed")
	}
	if !client.IsClosed() {
		t.Fatalf("session should have been closed")
	}
}
func TestCloseTimeout(t *testing.T) {
	const timeout = 10 * time.Millisecond
	conf := _conf()
	conf.TimeoutStreamClose = timeout
	client, server := _cs(conf)
	defer client.Close()
	defer server.Close()

	if client.NumStreams() != 0 {
		t.Fatalf("bad")
	}
	if server.NumStreams() != 0 {
		t.Fatalf("bad")
	}

	wg := &sync.WaitGroup{}
	wg.Add(2)

	var clientStream *Stream
	go func() {
		defer wg.Done()
		var err error
		clientStream, err = client.OpenStream()
		if err != nil {
			t.Errorf("err: %v", err)
			return
		}
	}()

	go func() {
		defer wg.Done()
		stream, err := server.AcceptStream()
		if err != nil {
			t.Errorf("err: %v", err)
			return
		}
		if err := stream.Close(); err != nil {
			t.Errorf("err: %v", err)
			return
		}
	}()

	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()

	select {
	case <-doneCh:
	case <-time.After(time.Second):
		panic("timeout")
	}

	time.Sleep(100 * time.Millisecond)

	if v := server.NumStreams(); v > 0 {
		t.Fatalf("should have zero streams: %d", v)
	}
	if v := client.NumStreams(); v > 0 {
		t.Fatalf("should have zero streams: %d", v)
	}

	if _, err := clientStream.Write([]byte("hello")); err == nil {
		t.Fatal("should error on write")
	} else if err.Error() != "connection reset" {
		t.Fatalf("expected connection reset, got %q", err)
	}
}
func TestNonNilInterface(t *testing.T) {
	_, server := _cs(nil)
	server.Close()

	conn, err := server.Accept()
	if err != nil && conn != nil {
		t.Error("bad: accept should return a connection of nil value")
	}
	conn, err = server.Open()
	if err != nil && conn != nil {
		t.Error("bad: open should return a connection of nil value")
	}
}
func TestSendDataSmall(t *testing.T) {
	client, server := _cs(nil)
	defer client.Close()
	defer server.Close()

	wg := &sync.WaitGroup{}
	wg.Add(2)

	go func() {
		defer wg.Done()
		stream, err := server.AcceptStream()
		if err != nil {
			t.Errorf("err: %v", err)
			return
		}

		if server.NumStreams() != 1 {
			t.Errorf("bad")
			return
		}

		buf := make([]byte, 4)
		for i := 0; i < 1000; i++ {
			n, err := stream.Read(buf)
			if err != nil {
				t.Errorf("err: %v", err)
				return
			}
			if n != 4 {
				t.Errorf("short read: %d", n)
				return
			}
			if string(buf) != "test" {
				t.Errorf("bad: %s", buf)
				return
			}
		}

		if err := stream.Close(); err != nil {
			t.Errorf("err: %v", err)
			return
		}
	}()

	go func() {
		defer wg.Done()
		stream, err := client.Open()
		if err != nil {
			t.Errorf("err: %v", err)
			return
		}

		if client.NumStreams() != 1 {
			t.Errorf("bad")
			return
		}

		for i := 0; i < 1000; i++ {
			n, err := stream.Write([]byte("test"))
			if err != nil {
				t.Errorf("err: %v", err)
				return
			}
			if n != 4 {
				t.Errorf("short write %d", n)
				return
			}
		}

		if err := stream.Close(); err != nil {
			t.Errorf("err: %v", err)
			return
		}
	}()

	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()
	select {
	case <-doneCh:
		if client.NumStreams() != 0 {
			t.Fatalf("bad")
		}
		if server.NumStreams() != 0 {
			t.Fatalf("bad")
		}
		return
	case <-time.After(time.Second):
		panic("timeout")
	}
}
func TestSendDataLarge(t *testing.T) {
	client, server := _cs(nil)
	defer client.Close()
	defer server.Close()

	const (
		sendSize = 250 * 1024 * 1024
		recvSize = 4 * 1024
	)

	data := make([]byte, sendSize)
	for idx := range data {
		data[idx] = byte(idx % 256)
	}

	wg := &sync.WaitGroup{}
	wg.Add(2)

	go func() {
		defer wg.Done()
		stream, err := server.AcceptStream()
		if err != nil {
			t.Errorf("err: %v", err)
			return
		}
		var sz int
		buf := make([]byte, recvSize)
		for i := 0; i < sendSize/recvSize; i++ {
			n, err := stream.Read(buf)
			if err != nil {
				t.Errorf("err: %v", err)
				return
			}
			if n != recvSize {
				t.Errorf("short read: %d", n)
				return
			}
			sz += n
			for idx := range buf {
				if buf[idx] != byte(idx%256) {
					t.Errorf("bad: %v %v %v", i, idx, buf[idx])
					return
				}
			}
		}

		if err := stream.Close(); err != nil {
			t.Errorf("err: %v", err)
			return
		}

		t.Logf("cap=%d, n=%d\n", stream.recvBuff.Cap(), sz)
	}()

	go func() {
		defer wg.Done()
		stream, err := client.Open()
		if err != nil {
			t.Errorf("err: %v", err)
			return
		}

		n, err := stream.Write(data)
		if err != nil {
			t.Errorf("err: %v", err)
			return
		}
		if n != len(data) {
			t.Errorf("short write %d", n)
			return
		}

		if err := stream.Close(); err != nil {
			t.Errorf("err: %v", err)
			return
		}
	}()
	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()
	select {
	case <-doneCh:
		return
	case <-time.After(5 * time.Second):
		panic("timeout")
	}
}
func TestGoAway(t *testing.T) {
	client, server := _cs(nil)
	defer client.Close()
	defer server.Close()

	if err := server.GoAway(); err != nil {
		t.Fatalf("err: %v", err)
	}
	_, err := client.Open()
	if !errors.Is(err, ErrRemoteGoAway) {
		t.Fatalf("err: %v", err)
	}
}
func TestManyStreams(t *testing.T) {
	client, server := _cs(nil)
	defer client.Close()
	defer server.Close()

	wg := &sync.WaitGroup{}

	acceptor := func(i int) {
		defer wg.Done()
		stream, err := server.AcceptStream()
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		defer stream.Close()

		buf := make([]byte, 512)
		for {
			n, err := stream.Read(buf)
			if err == io.EOF {
				return
			}
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if n == 0 {
				t.Fatalf("err: %v", err)
			}
		}
	}
	sender := func(i int) {
		defer wg.Done()
		stream, err := client.Open()
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		defer stream.Close()

		msg := fmt.Sprintf("%08d", i)
		for i := 0; i < 1000; i++ {
			n, err := stream.Write([]byte(msg))
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if n != len(msg) {
				t.Fatalf("short write %d", n)
			}
		}
	}

	for i := 0; i < 50; i++ {
		wg.Add(2)
		go acceptor(i)
		go sender(i)
	}

	wg.Wait()
}
func TestManyStreamsPingPong(t *testing.T) {
	client, server := _cs(nil)
	defer client.Close()
	defer server.Close()

	wg := &sync.WaitGroup{}

	ping := []byte("ping")
	pong := []byte("pong")

	acceptor := func(i int) {
		defer wg.Done()
		stream, err := server.AcceptStream()
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		defer stream.Close()

		buf := make([]byte, 4)
		for {
			// Read the 'ping'
			n, err := stream.Read(buf)
			if err == io.EOF {
				return
			}
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if n != 4 {
				t.Fatalf("err: %v", err)
			}
			if !bytes.Equal(buf, ping) {
				t.Fatalf("bad: %s", buf)
			}

			stream.Shrink()

			n, err = stream.Write(pong)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if n != 4 {
				t.Fatalf("err: %v", err)
			}
		}
	}
	sender := func(i int) {
		defer wg.Done()
		stream, err := client.OpenStream()
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		defer stream.Close()

		buf := make([]byte, 4)
		for i := 0; i < 1000; i++ {
			// Send the 'ping'
			n, err := stream.Write(ping)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if n != 4 {
				t.Fatalf("short write %d", n)
			}

			// Read the 'pong'
			n, err = stream.Read(buf)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if n != 4 {
				t.Fatalf("err: %v", err)
			}
			if !bytes.Equal(buf, pong) {
				t.Fatalf("bad: %s", buf)
			}

			// Shrink the buffer
			stream.Shrink()
		}
	}

	for i := 0; i < 50; i++ {
		wg.Add(2)
		go acceptor(i)
		go sender(i)
	}

	wg.Wait()
}
func TestHalfClose(t *testing.T) {
	conf := _conf()
	conf.MaxStream = 64
	conf.TimeoutWrite = 250 * time.Millisecond
	client, server := _cs(conf)
	defer client.Close()
	defer server.Close()

	stream, err := client.Open()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if _, err = stream.Write([]byte("a")); err != nil {
		t.Fatalf("err: %v", err)
	}

	stream2, err := server.Accept()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	stream2.Close() // Half close

	buf := make([]byte, 4)
	n, err := stream2.Read(buf)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n != 1 {
		t.Fatalf("bad: %v", n)
	}

	if _, err = stream.Write([]byte("bcd")); err != nil {
		t.Fatalf("err: %v", err)
	}
	stream.Close()

	n, err = stream2.Read(buf)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n != 3 {
		t.Fatalf("bad: %v", n)
	}

	n, err = stream2.Read(buf)
	if err != io.EOF {
		t.Fatalf("err: %v", err)
	}
	if n != 0 {
		t.Fatalf("bad: %v", n)
	}
}
func TestHalfCloseSessionShutdown(t *testing.T) {
	client, server := _cs(nil)
	defer client.Close()
	defer server.Close()

	dataSize := int64(server.conf.MaxStreamSize)

	data := make([]byte, dataSize)
	for idx := range data {
		data[idx] = byte(idx % 256)
	}

	stream, err := client.Open()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if _, err = stream.Write(data); err != nil {
		t.Fatalf("err: %v", err)
	}

	stream2, err := server.Accept()
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if err := stream.Close(); err != nil {
		t.Fatalf("err: %v", err)
	}

	// Shut down the session of the sending side. This should not cause reads
	// to fail on the receiving side.
	if err := client.Close(); err != nil {
		t.Fatalf("err: %v", err)
	}

	buf := make([]byte, dataSize)
	n, err := stream2.Read(buf)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if int64(n) != dataSize {
		t.Fatalf("bad: %v", n)
	}

	// EOF after close
	n, err = stream2.Read(buf)
	if err != io.EOF {
		t.Fatalf("err: %v", err)
	}
	if n != 0 {
		t.Fatalf("bad: %v", n)
	}
}
func TestReadDeadline(t *testing.T) {
	client, server := _cs(nil)
	defer client.Close()
	defer server.Close()

	stream, err := client.Open()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	defer stream.Close()

	stream2, err := server.Accept()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	defer stream2.Close()

	if err := stream.SetReadDeadline(time.Now().Add(5 * time.Millisecond)); err != nil {
		t.Fatalf("err: %v", err)
	}

	buf := make([]byte, 4)
	_, err = stream.Read(buf)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err: %v", err)
	}

	if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("reading timeout error is expected to implement net.Error and return true when calling Timeout()")
	}
}
func TestReadDeadlineBlockedRead(t *testing.T) {
	client, server := _cs(nil)
	defer client.Close()
	defer server.Close()

	stream, err := client.Open()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	defer stream.Close()

	stream2, err := server.Accept()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	defer stream2.Close()

	// Start a read that will block
	errCh := make(chan error, 1)
	go func() {
		buf := make([]byte, 4)
		_, err := stream.Read(buf)
		errCh <- err
		close(errCh)
	}()

	// Wait to ensure the read has started.
	time.Sleep(5 * time.Millisecond)

	// Update the read deadline
	if err := stream.SetReadDeadline(time.Now().Add(5 * time.Millisecond)); err != nil {
		t.Fatalf("err: %v", err)
	}

	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected read timeout")
	case err := <-errCh:
		if err != ErrTimeout {
			t.Fatalf("expected ErrTimeout; got %v", err)
		}
	}
}
func TestWriteDeadline(t *testing.T) {
	client, server := _cs(nil)
	defer client.Close()
	defer server.Close()

	stream, err := client.Open()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	defer stream.Close()

	stream2, err := server.Accept()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	defer stream2.Close()

	if err := stream.SetWriteDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("err: %v", err)
	}

	buf := make([]byte, 512)
	for i := 0; i < int(defaultMaxStreamSize); i++ {
		_, err := stream.Write(buf)
		if err != nil && errors.Is(err, ErrTimeout) {
			return
		} else if err != nil {
			t.Fatalf("err: %v", err)
		}
	}
	t.Fatalf("Expected timeout")
}
func TestWriteDeadlineBlockedWrite(t *testing.T) {
	client, server := _cs(nil)
	defer client.Close()
	defer server.Close()

	stream, err := client.Open()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	defer stream.Close()

	stream2, err := server.Accept()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	defer stream2.Close()

	// Start a goroutine making writes that will block
	errCh := make(chan error, 1)
	go func() {
		buf := make([]byte, 512)
		for i := 0; i < int(defaultMaxStreamSize); i++ {
			_, err := stream.Write(buf)
			if err == nil {
				continue
			}

			errCh <- err
			close(errCh)
			return
		}

		close(errCh)
	}()

	// Wait to ensure the write has started.
	time.Sleep(5 * time.Millisecond)

	// Update the write deadline
	if err := stream.SetWriteDeadline(time.Now().Add(5 * time.Millisecond)); err != nil {
		t.Fatalf("err: %v", err)
	}

	select {
	case <-time.After(1 * time.Second):
		t.Fatal("expected write timeout")
	case err := <-errCh:
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("expected ErrTimeout; got %v", err)
		}
	}
}
func TestBacklogExceeded(t *testing.T) {
	client, server := _cs(nil)
	defer client.Close()
	defer server.Close()

	// Fill the backlog
	max := client.conf.MaxStream
	for i := 0; i < max; i++ {
		stream, err := client.Open()
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		defer stream.Close()

		if _, err := stream.Write([]byte("foo")); err != nil {
			t.Fatalf("err: %v", err)
		}
	}

	// Attempt to open a new stream
	errCh := make(chan error, 1)
	go func() {
		_, err := client.Open()
		errCh <- err
	}()

	// Shutdown the server
	go func() {
		time.Sleep(10 * time.Millisecond)
		server.Close()
	}()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatalf("open should fail")
		}
	case <-time.After(time.Second):
		t.Fatalf("timeout")
	}
}
func TestKeepAlive(t *testing.T) {
	client, server := _cs(nil)
	defer client.Close()
	defer server.Close()

	time.Sleep(200 * time.Millisecond)

	// Ping value should increase
	client.pingLock.Lock()
	defer client.pingLock.Unlock()
	if client.pingID == 0 {
		t.Fatalf("should ping")
	}

	server.pingLock.Lock()
	defer server.pingLock.Unlock()
	if server.pingID == 0 {
		t.Fatalf("should ping")
	}
}
func TestKeepAliveTimeout(t *testing.T) {
	conn1, conn2 := _conn()
	clientConf := _conf()
	clientConf.TimeoutWrite = time.Hour // We're testing keep alives, not connection writes
	clientConf.KeepAlive = Ptr(false)   // Just test one direction, so it's deterministic who hangs up on whom
	client := Client(conn1, clientConf)
	defer client.Close()

	server := Server(conn2, _conf())
	defer server.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := server.Accept() // Wait until server closes
		errCh <- err
	}()

	// Prevent the client from responding
	clientConn := client.conn.(*pipeConn)
	clientConn.writeBlocker.Lock()

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrKeepAliveTimeout) {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timeout waiting for timeout")
	}

	clientConn.writeBlocker.Unlock()

	if !server.IsClosed() {
		t.Fatalf("server should have closed")
	}
}
func TestLargeSize(t *testing.T) {
	conf := _conf()
	conf.MaxStreamSize *= 2

	client, server := _cs(conf)
	defer client.Close()
	defer server.Close()

	stream, err := client.Open()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	defer stream.Close()

	stream2, err := server.Accept()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	defer stream2.Close()

	_ = stream.SetWriteDeadline(time.Now().Add(10 * time.Millisecond))
	buf := make([]byte, conf.MaxStreamSize)
	n, err := stream.Write(buf)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n != len(buf) {
		t.Fatalf("short write: %d", n)
	}
}

type UnlimitedReader struct{}

func (u *UnlimitedReader) Read(p []byte) (int, error) {
	runtime.Gosched()
	return len(p), nil
}
func TestSendDataVeryLarge(t *testing.T) {
	client, server := _cs(nil)
	defer client.Close()
	defer server.Close()

	var n int64 = 1 * 1024 * 1024 * 1024
	var workers int = 16

	wg := &sync.WaitGroup{}
	wg.Add(workers * 2)

	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			stream, err := server.AcceptStream()
			if err != nil {
				t.Errorf("err: %v", err)
				return
			}
			defer stream.Close()

			buf := make([]byte, 4)
			_, err = stream.Read(buf)
			if err != nil {
				t.Errorf("err: %v", err)
				return
			}
			if !bytes.Equal(buf, []byte{0, 1, 2, 3}) {
				t.Errorf("bad header")
				return
			}

			recv, err := io.Copy(io.Discard, stream)
			if err != nil {
				t.Errorf("err: %v", err)
				return
			}
			if recv != n {
				t.Errorf("bad: %v", recv)
				return
			}
		}()
	}
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			stream, err := client.Open()
			if err != nil {
				t.Errorf("err: %v", err)
				return
			}
			defer stream.Close()

			_, err = stream.Write([]byte{0, 1, 2, 3})
			if err != nil {
				t.Errorf("err: %v", err)
				return
			}

			unlimited := &UnlimitedReader{}
			sent, err := io.Copy(stream, io.LimitReader(unlimited, n))
			if err != nil {
				t.Errorf("err: %v", err)
				return
			}
			if sent != n {
				t.Errorf("bad: %v", sent)
				return
			}
		}()
	}

	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()
	select {
	case <-doneCh:
	case <-time.After(20 * time.Second):
		panic("timeout")
	}
}
func TestBacklogExceededAccept(t *testing.T) {
	client, server := _cs(nil)
	defer client.Close()
	defer server.Close()

	max := 5 * client.conf.MaxStream
	go func() {
		for i := 0; i < max; i++ {
			stream, err := server.Accept()
			if err != nil {
				t.Errorf("err: %v", err)
				return
			}
			defer stream.Close()
		}
	}()

	// Fill the backlog
	for i := 0; i < max; i++ {
		stream, err := client.Open()
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		defer stream.Close()

		if _, err := stream.Write([]byte("foo")); err != nil {
			t.Fatalf("err: %v", err)
		}
	}
}
func TestSessionUpdateWriteDuringRead(t *testing.T) {
	conf := _conf()
	conf.KeepAlive = Ptr(false)
	client, server := _cs(conf)
	defer client.Close()
	defer server.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	// Choose a huge flood size that we know will result in a window update.
	flood := int64(client.conf.MaxStreamSize) - 1

	// The server will accept a new stream and then flood data to it.
	go func() {
		defer wg.Done()

		stream, err := server.AcceptStream()
		if err != nil {
			t.Errorf("err: %v", err)
			return
		}
		defer stream.Close()

		n, err := stream.Write(make([]byte, flood))
		if err != nil {
			t.Errorf("err: %v", err)
			return
		}
		if int64(n) != flood {
			t.Errorf("short write: %d", n)
			return
		}
	}()

	// The client will open a stream, block outbound writes, and then
	// listen to the flood from the server, which should time out since
	// it won't be able to send the window update.
	go func() {
		defer wg.Done()

		stream, err := client.OpenStream()
		if err != nil {
			t.Errorf("err: %v", err)
			return
		}
		defer stream.Close()

		conn := client.conn.(*pipeConn)
		conn.writeBlocker.Lock()
		defer conn.writeBlocker.Unlock()

		_, err = stream.Read(make([]byte, flood))
		if !errors.Is(err, ErrTimeout) {
			t.Errorf("err: %v", err)
			return
		}
	}()

	wg.Wait()
}
func TestSessionPartialReadWindowUpdate(t *testing.T) {
	conf := _conf()
	conf.KeepAlive = Ptr(false)
	client, server := _cs(conf)
	defer client.Close()
	defer server.Close()

	var wg sync.WaitGroup
	wg.Add(1)

	// Choose a huge flood size that we know will result in a window update.
	flood := int64(client.conf.MaxStreamSize)
	var wr *Stream

	// The server will accept a new stream and then flood data to it.
	go func() {
		defer wg.Done()

		var err error
		wr, err = server.AcceptStream()
		if err != nil {
			t.Errorf("err: %v", err)
			return
		}
		defer wr.Close()

		if wr.sendSize != client.conf.MaxStreamSize {
			t.Errorf("sendWindow: exp=%d, got=%d", client.conf.MaxStreamSize, wr.sendSize)
			return
		}

		n, err := wr.Write(make([]byte, flood))
		if err != nil {
			t.Errorf("err: %v", err)
			return
		}
		if int64(n) != flood {
			t.Errorf("short write: %d", n)
			return
		}
		if wr.sendSize != 0 {
			t.Errorf("sendWindow: exp=%d, got=%d", 0, wr.sendSize)
			return
		}
	}()

	stream, err := client.OpenStream()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	defer stream.Close()

	wg.Wait()

	_, err = stream.Read(make([]byte, flood/2+1))

	if exp := uint32(flood/2 + 1); wr.sendSize != exp {
		t.Errorf("sendWindow: exp=%d, got=%d", exp, wr.sendSize)
	}
}
func TestSessionSendWithoutWaitTimeout(t *testing.T) {
	conf := _conf()
	conf.KeepAlive = Ptr(false)
	client, server := _cs(conf)
	defer client.Close()
	defer server.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()

		stream, err := server.AcceptStream()
		if err != nil {
			t.Errorf("err: %v", err)
			return
		}
		defer stream.Close()
	}()

	// The client will open the stream and then block outbound writes, we'll
	// probe sendNoWait once it gets into that state.
	go func() {
		defer wg.Done()

		stream, err := client.OpenStream()
		if err != nil {
			t.Errorf("err: %v", err)
			return
		}
		defer stream.Close()

		conn := client.conn.(*pipeConn)
		conn.writeBlocker.Lock()
		defer conn.writeBlocker.Unlock()

		hdr := header(make([]byte, headerSize))
		hdr.encode(msgTypePing, flagACK, 0, 0)
		for {
			err = client.sendWithoutWait(hdr)
			if err == nil {
				continue
			} else if errors.Is(err, ErrTimeout) {
				break
			} else {
				t.Errorf("err: %v", err)
				return
			}
		}
	}()

	wg.Wait()
}
func TestSessionPingOfDeath(t *testing.T) {
	conf := _conf()
	conf.KeepAlive = Ptr(false)
	client, server := _cs(conf)
	defer client.Close()
	defer server.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	var doPingOfDeath sync.Mutex
	doPingOfDeath.Lock()

	// This is used later to block outbound writes.
	conn := server.conn.(*pipeConn)

	// The server will accept a stream, block outbound writes, and then
	// flood its send channel so that no more headers can be queued.
	go func() {
		defer wg.Done()

		stream, err := server.AcceptStream()
		if err != nil {
			t.Errorf("err: %v", err)
			return
		}
		defer stream.Close()

		conn.writeBlocker.Lock()
		for {
			hdr := header(make([]byte, headerSize))
			hdr.encode(msgTypePing, 0, 0, 0)
			err = server.sendWithoutWait(hdr)
			if err == nil {
				continue
			} else if errors.Is(err, ErrTimeout) {
				break
			} else {
				t.Errorf("err: %v", err)
				return
			}
		}

		doPingOfDeath.Unlock()
	}()

	// The client will open a stream and then send the server a ping once it
	// can no longer write. This makes sure the server doesn't deadlock reads
	// while trying to reply to the ping with no ability to write.
	go func() {
		defer wg.Done()

		stream, err := client.OpenStream()
		if err != nil {
			t.Errorf("err: %v", err)
			return
		}
		defer stream.Close()

		// This ping will never unblock because the ping id will never
		// show up in a response.
		doPingOfDeath.Lock()
		go func() { _, _ = client.Ping() }()

		// Wait for a while to make sure the previous ping times out,
		// then turn writes back on and make sure a ping works again.
		time.Sleep(2 * server.conf.TimeoutWrite)
		conn.writeBlocker.Unlock()
		if _, err = client.Ping(); err != nil {
			t.Errorf("err: %v", err)
			return
		}
	}()

	wg.Wait()
}
func TestSessionConnectionWriteTimeout(t *testing.T) {
	conf := _conf()
	conf.KeepAlive = Ptr(false)
	client, server := _cs(conf)
	defer client.Close()
	defer server.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()

		stream, err := server.AcceptStream()
		if err != nil {
			t.Errorf("err: %v", err)
			return
		}
		defer stream.Close()
	}()

	// The client will open the stream and then block outbound writes, we'll
	// tee up a write and make sure it eventually times out.
	go func() {
		defer wg.Done()

		stream, err := client.OpenStream()
		if err != nil {
			t.Errorf("err: %v", err)
			return
		}
		defer stream.Close()

		conn := client.conn.(*pipeConn)
		conn.writeBlocker.Lock()
		defer conn.writeBlocker.Unlock()

		// Since the write goroutine is blocked then this will return a
		// timeout since it can't get feedback about whether the write
		// worked.
		n, err := stream.Write([]byte("hello"))
		if !errors.Is(err, ErrTimeout) {
			t.Errorf("err: %v", err)
			return
		}
		if n != 0 {
			t.Errorf("lied about writes: %d", n)
			return
		}
	}()

	wg.Wait()
}
func TestCancelAccept(t *testing.T) {
	client, server := _cs(nil)
	defer client.Close()
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()

		stream, err := server.AcceptStreamWithContext(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err: %v", err)
			return
		}

		if stream != nil {
			defer stream.Close()
		}
	}()

	cancel()

	wg.Wait()
}
