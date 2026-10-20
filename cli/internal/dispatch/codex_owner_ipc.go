package dispatch

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const codexOwnerIPCMaxFrame = 8 << 20

var errCodexOwnerIPCUnsupported = errors.New("main_notice_owner_ipc_unsupported")

// The caller owns notice admission and uncertainty. This transport sends each
// request once, never reconnects, and never selects or starts a Desktop owner.
type codexOwnerIPC struct {
	conn      net.Conn
	clientID  string
	admission chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func newCodexOwnerIPC(conn net.Conn) *codexOwnerIPC {
	return &codexOwnerIPC{conn: conn, clientID: "initializing-client", admission: make(chan struct{}, 1)}
}

func openCodexOwnerIPC(ctx context.Context, binding Binding) (*codexOwnerIPC, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if binding.Host != "codex" || binding.Mode != "native" {
		return nil, errors.New("main_notice_owner_binding_invalid")
	}
	conn, err := dialCodexOwnerIPC(ctx, binding)
	if err != nil {
		return nil, err
	}
	c := newCodexOwnerIPC(conn)
	raw, handledBy, err := c.call(ctx, "initialize", 0, map[string]string{"clientType": "external-agent"}, "")
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	var initialized struct {
		ClientID string `json:"clientId"`
	}
	if json.Unmarshal(raw, &initialized) != nil || !validCodexOwnerIPCIdentifier(initialized.ClientID) || handledBy != "" && initialized.ClientID != handledBy {
		_ = c.Close()
		return nil, errors.New("main_notice_owner_initialize_invalid")
	}
	c.clientID = initialized.ClientID
	return c, nil
}

func (c *codexOwnerIPC) Close() error {
	c.closeOnce.Do(func() { c.closeErr = c.conn.Close() })
	return c.closeErr
}

func validCodexOwnerIPCIdentifier(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) <= 256 && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func codexOwnerIPCRequestID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", errors.New("main_notice_owner_request_id_failed")
	}
	random[6], random[8] = random[6]&0x0f|0x40, random[8]&0x3f|0x80
	text := hex.EncodeToString(random[:])
	return text[:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:], nil
}

// call serializes one absolute-deadline exchange. Cancellation or any malformed
// response closes this connection; a caller cannot accidentally reuse its stream.
func (c *codexOwnerIPC) call(ctx context.Context, method string, version int, params any, owner string) (result json.RawMessage, handledBy string, callErr error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	select {
	case c.admission <- struct{}{}:
		defer func() { <-c.admission }()
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	defer func() {
		if callErr != nil {
			_ = c.Close()
		}
	}()
	if !validCodexOwnerIPCIdentifier(method) || version < 0 || owner != "" && !validCodexOwnerIPCIdentifier(owner) || !validCodexOwnerIPCIdentifier(c.clientID) {
		return nil, "", errors.New("main_notice_owner_request_invalid")
	}
	deadline := time.Now().Add(30 * time.Second)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return nil, "", errors.New("main_notice_owner_deadline_failed")
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	id, err := codexOwnerIPCRequestID()
	if err != nil {
		return nil, "", err
	}
	request := map[string]any{"type": "request", "requestId": id, "sourceClientId": c.clientID, "method": method, "version": version, "params": params, "timeoutMs": max(int64(1), time.Until(deadline).Milliseconds())}
	if owner != "" {
		request["targetClientId"] = owner
	}
	if err := writeCodexOwnerIPCFrame(c.conn, request); err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", errors.New("main_notice_owner_write_unconfirmed")
	}
	for {
		raw, err := readCodexOwnerIPCFrame(c.conn)
		if err != nil {
			if ctx.Err() != nil {
				return nil, "", ctx.Err()
			}
			return nil, "", errors.New("main_notice_owner_response_unconfirmed")
		}
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		var message struct {
			Type       string          `json:"type"`
			RequestID  string          `json:"requestId"`
			Source     string          `json:"sourceClientId"`
			Method     string          `json:"method"`
			ResultType string          `json:"resultType"`
			HandledBy  string          `json:"handledByClientId"`
			Result     json.RawMessage `json:"result"`
		}
		if decodeStrictObject(raw, &message) != nil {
			// Desktop envelopes may grow independent fields; reject duplicate
			// keys but retain forward-compatible fields through a raw map.
			var envelope map[string]json.RawMessage
			if decodeStrictObject(raw, &envelope) != nil || json.Unmarshal(raw, &message) != nil {
				return nil, "", errors.New("main_notice_owner_message_invalid")
			}
		}
		switch message.Type {
		case "client-discovery-request":
			if !validCodexOwnerIPCIdentifier(message.RequestID) {
				return nil, "", errors.New("main_notice_owner_discovery_invalid")
			}
			if err := writeCodexOwnerIPCFrame(c.conn, map[string]any{"type": "client-discovery-response", "requestId": message.RequestID, "response": map[string]bool{"canHandle": false}}); err != nil {
				return nil, "", errors.New("main_notice_owner_response_unconfirmed")
			}
		case "broadcast":
			continue
		case "response":
			if message.RequestID != id {
				return nil, "", errors.New("main_notice_owner_request_mismatch")
			}
			if message.ResultType != "success" {
				return nil, "", errors.New("main_notice_owner_request_failed")
			}
			initialized := method == "initialize" && owner == ""
			if message.Method != method || (!initialized || message.HandledBy != "") && !validCodexOwnerIPCIdentifier(message.HandledBy) || owner != "" && message.HandledBy != owner || message.Source != "" && message.Source != message.HandledBy || len(message.Result) == 0 {
				return nil, "", errors.New("main_notice_owner_response_mismatch")
			}
			return append(json.RawMessage(nil), message.Result...), message.HandledBy, nil
		default:
			return nil, "", errors.New("main_notice_owner_message_unsupported")
		}
	}
}

func writeCodexOwnerIPCFrame(writer io.Writer, value any) error {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) == 0 || len(raw) > codexOwnerIPCMaxFrame {
		return errors.New("invalid_owner_frame")
	}
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(raw)))
	for _, part := range [][]byte{header[:], raw} {
		for len(part) > 0 {
			n, err := writer.Write(part)
			if n < 0 || n > len(part) {
				return io.ErrShortWrite
			}
			part = part[n:]
			if err != nil {
				return err
			}
			if n == 0 {
				return io.ErrShortWrite
			}
		}
	}
	return nil
}

func readCodexOwnerIPCFrame(reader io.Reader) (json.RawMessage, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	size := binary.LittleEndian.Uint32(header[:])
	if size == 0 || size > codexOwnerIPCMaxFrame {
		return nil, errors.New("invalid_owner_frame_size")
	}
	raw := make([]byte, int(size))
	if _, err := io.ReadFull(reader, raw); err != nil {
		return nil, err
	}
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return nil, errors.New("invalid_owner_frame_json")
	}
	return raw, nil
}
