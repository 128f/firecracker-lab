package vm

import (
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/128f/fctl/agentpb"
	"google.golang.org/protobuf/proto"
)

// guestAgentPort is the vsock port the guest agent listens on for the
// status/control protocol (agent.proto), distinct from the interactive
// shell port used by DialVsock in cmd vsock.
const guestAgentPort = 1234

// Status queries the guest agent's health over vsock. It returns an error
// if the VM isn't reachable (not running, agent not up yet, etc.).
func (r *Runner) Status(id string) (agentpb.HealthStatus, error) {
	conn, err := r.DialVsock(id, guestAgentPort)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))

	req := &agentpb.Request{
		RequestType: &agentpb.Request_Status{Status: &agentpb.StatusRequest{}},
	}
	if err := writeFramed(conn, req); err != nil {
		return 0, fmt.Errorf("send status request: %w", err)
	}

	resp := &agentpb.StatusResponse{}
	if err := readFramed(conn, resp); err != nil {
		return 0, fmt.Errorf("read status response: %w", err)
	}
	return resp.GetStatus(), nil
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
