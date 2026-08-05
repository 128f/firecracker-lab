package vm

import (
	"bufio"
	"fmt"
	"net"
	"strings"
)

// vsockConn is a net.Conn that reads through a bufio.Reader, so bytes
// buffered while parsing the CONNECT handshake reply aren't lost.
type vsockConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *vsockConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

// DialVsock connects to a VM's vsock host socket and performs the
// Firecracker UDS-backend CONNECT handshake for the given guest port,
// returning a net.Conn ready for use once the handshake succeeds.
func (r *Runner) DialVsock(id string, port uint32) (net.Conn, error) {
	sock := r.VsockPath(id)
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("connect to vsock: %w", err)
	}

	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send CONNECT: %w", err)
	}

	br := bufio.NewReader(conn)
	reply, err := br.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read CONNECT reply: %w", err)
	}
	reply = strings.TrimSpace(reply)
	if !strings.HasPrefix(reply, "OK") {
		conn.Close()
		return nil, fmt.Errorf("vsock CONNECT %d rejected: %s", port, reply)
	}

	return &vsockConn{Conn: conn, r: br}, nil
}
