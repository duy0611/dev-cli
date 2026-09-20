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

// The two agent protocol messages needed to list identities (RFC 4251 §5 wire
// format, draft-miller-ssh-agent). Asking the agent directly rather than
// shelling out to `ssh-add -L`: that binary is not guaranteed to be on the
// host's PATH, and parsing its output would be a second format to get wrong.
const (
	agentRequestIdentities = 11
	agentIdentitiesAnswer  = 12
)

// identityTimeout bounds the exchange. A wedged or hostile agent must not hang
// a command that was only going to sign a commit.
const identityTimeout = 5 * time.Second

// SigningKey returns the first public key the host's agent holds, in the
// `key::ssh-ed25519 AAAA...` form git wants for user.signingkey.
//
// The first, because an agent gives no indication which of its keys the
// operator would want to sign with, and asking would be a question most people
// have one answer to. An agent holding several may sign with one the forge does
// not know, which shows up as an unverified commit rather than a failure.
//
// The `key::` prefix is not optional: without it git reads the value as a
// *filename* and fails looking for a key file that does not exist. A bare
// `ssh-...` value is accepted as a deprecated spelling, but the explicit prefix
// is what the documentation asks for.
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

	// uint32 count, then per key: a string blob and a string comment.
	if binary.BigEndian.Uint32(reply[1:5]) == 0 {
		return "", errors.New("the ssh agent holds no keys; add one with: ssh-add")
	}
	blob, _, err := readString(reply[5:])
	if err != nil {
		return "", fmt.Errorf("reading the ssh agent's keys: %w", err)
	}

	// The key's own type name is the first string inside the blob, and it is
	// also the prefix of the authorized_keys form git expects.
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
