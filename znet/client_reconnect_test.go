package znet

import (
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aceld/zinx/ziface"
)

type reconnectTestServer struct {
	listener net.Listener
	stopCh   chan struct{}
	connMu   sync.Mutex
	conn     net.Conn
}

func startReconnectTestServer(t *testing.T, port int) *reconnectTestServer {
	t.Helper()

	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", intToString(port)))
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}

	s := &reconnectTestServer{
		listener: ln,
		stopCh:   make(chan struct{}),
	}

	go func() {
		for {
			conn, err := s.listener.Accept()
			if err != nil {
				return
			}
			s.connMu.Lock()
			if s.conn != nil {
				_ = s.conn.Close()
			}
			s.conn = conn
			s.connMu.Unlock()

			go func(c net.Conn) {
				<-s.stopCh
				_ = c.Close()
			}(conn)
		}
	}()

	return s
}

func (s *reconnectTestServer) CloseActiveConn() {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
}

func (s *reconnectTestServer) Stop() {
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
	_ = s.listener.Close()
	s.CloseActiveConn()
}

func waitUntil(timeout, interval time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(interval)
	}
	return cond()
}

func getAvailableTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func intToString(n int) string {
	return strconv.Itoa(n)
}

func TestClientReconnectWhenServerRecovers(t *testing.T) {
	port := getAvailableTCPPort(t)

	var onStartCount int32
	client := NewClient("127.0.0.1", port, WithAutoReconnect(true), WithReconnectInterval(100*time.Millisecond), WithMaxReconnectAttempts(0))
	client.SetOnConnStart(func(conn ziface.IConnection) {
		atomic.AddInt32(&onStartCount, 1)
	})

	client.Start()
	defer client.Stop()

	time.Sleep(200 * time.Millisecond)

	server := startReconnectTestServer(t, port)
	defer server.Stop()

	ok := waitUntil(5*time.Second, 50*time.Millisecond, func() bool {
		return atomic.LoadInt32(&onStartCount) >= 1
	})
	if !ok {
		t.Fatalf("client did not reconnect when server recovered")
	}
}

func TestClientReconnectAfterConnectionClosed(t *testing.T) {
	port := getAvailableTCPPort(t)

	var onStartCount int32
	client := NewClient("127.0.0.1", port, WithAutoReconnect(true), WithReconnectInterval(100*time.Millisecond), WithMaxReconnectAttempts(0))
	client.SetOnConnStart(func(conn ziface.IConnection) {
		atomic.AddInt32(&onStartCount, 1)
	})

	server1 := startReconnectTestServer(t, port)
	client.Start()
	defer client.Stop()

	ok := waitUntil(5*time.Second, 50*time.Millisecond, func() bool {
		return atomic.LoadInt32(&onStartCount) >= 1
	})
	if !ok {
		server1.Stop()
		t.Fatalf("client did not establish initial connection")
	}

	server1.Stop()
	time.Sleep(200 * time.Millisecond)

	server2 := startReconnectTestServer(t, port)
	defer server2.Stop()

	ok = waitUntil(5*time.Second, 50*time.Millisecond, func() bool {
		return atomic.LoadInt32(&onStartCount) >= 2
	})
	if !ok {
		t.Fatalf("client did not reconnect after connection closed")
	}
}
