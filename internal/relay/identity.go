package relay

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// The agent protocol messages for listing identities. The agent is asked
// directly, since ssh-add is not guaranteed to be on the host's PATH.
const (
	agentRequestIdentities = 11
	agentIdentitiesAnswer  = 12
)

// identityTimeout bounds the exchange, so a wedged agent cannot hang the
// command.
const identityTimeout = 5 * time.Second

// SigningKey returns the first public key the host's agent holds, in the
// `key::ssh-ed25519 AAAA...` form git wants for user.signingkey. The `key::`
// prefix matters: without it git reads the value as a filename.
func SigningKey(agentSocket string) (string, error) {
	conn, err := net.DialTimeout("unix", agentSocket, identityTimeout)
	if err != nil {
		return "", fmt.Errorf("reaching the ssh agent: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(identityTimeout)); err != nil {
		return "", err
	}

	if err := writeAgentMessage(conn, []byte{agentRequestIdentities}); err != nil {
		return "", fmt.Errorf("asking the ssh agent for its keys: %w", err)
	}
	reply, err := readAgentMessage(conn)
	if err != nil {
		return "", fmt.Errorf("reading the ssh agent's keys: %w", err)
	}
	if len(reply) < 5 || reply[0] != agentIdentitiesAnswer {
		return "", errors.New("the ssh agent gave an unexpected answer when asked for its keys")
	}

	// Count, then per key a blob and a comment; only the first key is used.
	if binary.BigEndian.Uint32(reply[1:5]) == 0 {
		return "", errors.New("the ssh agent holds no keys; add one with: ssh-add")
	}
	blob, _, err := readString(reply[5:])
	if err != nil {
		return "", fmt.Errorf("reading the ssh agent's keys: %w", err)
	}

	// The key type is the first string inside the blob.
	algo, _, err := readString(blob)
	if err != nil {
		return "", fmt.Errorf("reading the ssh agent's keys: %w", err)
	}

	return fmt.Sprintf("key::%s %s", algo, base64.StdEncoding.EncodeToString(blob)), nil
}

// writeAgentMessage frames a payload with its length, as the agent expects.
func writeAgentMessage(w io.Writer, payload []byte) error {
	buf := make([]byte, 4, 4+len(payload))
	binary.BigEndian.PutUint32(buf, uint32(len(payload)))
	_, err := w.Write(append(buf, payload...))
	return err
}

// readAgentMessage reads one length-prefixed reply.
func readAgentMessage(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	// An agent's identity list is small. The bound stops a corrupt or hostile
	// length from turning into an arbitrary allocation.
	if n == 0 || n > maxPayload {
		return nil, fmt.Errorf("the ssh agent sent a %d byte reply", n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// readString reads one uint32-length-prefixed field, returning it and the rest.
func readString(b []byte) (value, rest []byte, err error) {
	if len(b) < 4 {
		return nil, nil, errors.New("truncated field")
	}
	n := binary.BigEndian.Uint32(b[:4])
	if uint64(n) > uint64(len(b)-4) {
		return nil, nil, errors.New("field longer than the message")
	}
	return b[4 : 4+n], b[4+n:], nil
}
