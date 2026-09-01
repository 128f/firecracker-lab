package vm

import (
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/128f/labctl/agentpb"
	"google.golang.org/protobuf/proto"
)

// guestAgentPort is the vsock port the guest agent listens on for the
// framed status/control protocol (agent.proto).
const guestAgentPort = 1234

// guestPtyPort is the vsock port the guest agent listens on for interactive
// PTY sessions: no framing, raw bytes straight to/from a freshly spawned
// process's pty. Each connection gets its own process; closing the
// connection hangs up (and reaps) the child.
const guestPtyPort = 1235

// vsockCIDHost is VMADDR_CID_HOST, the well-known vsock CID a guest uses to
// reach the host.
const vsockCIDHost = 2

// Status queries the guest agent over vsock for its health, cpu/mem usage,
// and last heartbeat. It returns an error if the VM isn't reachable (not
// running, agent not up yet, etc.).
func (r *Runner) Status(id string) (*agentpb.StatusResponse, error) {
	conn, err := r.DialVsock(id, guestAgentPort)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))

	req := &agentpb.Request{
		RequestType: &agentpb.Request_Status{Status: &agentpb.StatusRequest{}},
	}
	resp, err := sendRequest(conn, req)
	if err != nil {
		return nil, fmt.Errorf("status request: %w", err)
	}
	return resp.GetStatus(), nil
}

// NotifyRestore tells the guest agent that this VM was just resumed from a
// snapshot, passing the current wall-clock time so it can reset the guest's
// system clock (which is frozen at snapshot time until told otherwise).
func (r *Runner) NotifyRestore(id string) error {
	conn, err := r.DialVsock(id, guestAgentPort)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))

	req := &agentpb.Request{
		RequestType: &agentpb.Request_VmRestore{
			VmRestore: &agentpb.VmRestoreNotification{
				RestoredAt: uint64(time.Now().Unix()),
			},
		},
	}
	if _, err := sendRequest(conn, req); err != nil {
		return fmt.Errorf("restore notification: %w", err)
	}
	return nil
}

// StartTcpVsockProxy tells vm id's guest agent to open a TCP listener on
// port inside the guest that forwards connections over vsock to the host
// (cid=2) at the same port — the guest side of the same port forward whose
// host side is a systemdPortForwarder unit bridging vsock.sock_<port> to
// 127.0.0.1:<port>.
func (r *Runner) StartTcpVsockProxy(id string, port int) error {
	conn, err := r.DialVsock(id, guestAgentPort)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))

	req := &agentpb.Request{
		RequestType: &agentpb.Request_StartTcpVsockProxy{
			StartTcpVsockProxy: &agentpb.StartTcpVsockProxy{
				TcpPort:   uint32(port),
				Cid:       vsockCIDHost,
				VsockPort: uint32(port),
			},
		},
	}
	if _, err := sendRequest(conn, req); err != nil {
		return fmt.Errorf("start tcp-vsock proxy for port %d: %w", port, err)
	}
	return nil
}

// StopTcpVsockProxy tells vm id's guest agent to close the TCP listener on
// port it opened via StartTcpVsockProxy.
func (r *Runner) StopTcpVsockProxy(id string, port int) error {
	conn, err := r.DialVsock(id, guestAgentPort)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))

	req := &agentpb.Request{
		RequestType: &agentpb.Request_StopTcpVsockProxy{
			StopTcpVsockProxy: &agentpb.StopTcpVsockProxy{TcpPort: uint32(port)},
		},
	}
	if _, err := sendRequest(conn, req); err != nil {
		return fmt.Errorf("stop tcp-vsock proxy for port %d: %w", port, err)
	}
	return nil
}

// sendRequest sends req over conn and returns the guest agent's decoded
// Response, translating a guest-reported Error into a Go error.
func sendRequest(conn io.ReadWriter, req *agentpb.Request) (*agentpb.Response, error) {
	if err := writeFramed(conn, req); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	resp := &agentpb.Response{}
	if err := readFramed(conn, resp); err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if e := resp.GetError(); e != nil {
		return nil, fmt.Errorf("guest agent: %s", e.GetReason())
	}
	return resp, nil
}

// writeFramed encodes m as a 4-byte big-endian length prefix followed by
// its protobuf bytes, matching the guest agent's framing.
func writeFramed(w io.Writer, m proto.Message) error {
	data, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(data)))
	if _, err := w.Write(prefix[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// readFramed reads a 4-byte big-endian length prefix followed by that many
// protobuf bytes, and unmarshals them into m.
func readFramed(r io.Reader, m proto.Message) error {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(prefix[:])
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	return proto.Unmarshal(data, m)
}
