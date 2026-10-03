package workers

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// A minimal RFC 6455 WebSocket client, enough for `wrangler tail`'s
// trace-v1 protocol: text frames in, small text frames and pings out.
//
// The tail socket is not a Cloudflare API request (its URL carries its own
// one-time credential; the API token is never sent on it) and it is a GET, so
// the read-only guard would allow it anyway. It is long-lived, which is why it
// can't use api.NewHTTPClient's per-attempt timeout.

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// WSConn is a client WebSocket connection.
type WSConn struct {
	conn net.Conn
	br   *bufio.Reader
	wmu  sync.Mutex
	// Protocol is the subprotocol the server picked.
	Protocol string
}

// ErrWSClosed is returned by ReadMessage after a close frame.
var ErrWSClosed = errors.New("websocket closed")

// DialWS opens a WebSocket to rawURL (ws://, wss://, http:// or https://).
func DialWS(ctx context.Context, rawURL, protocol string, header http.Header, handshakeTimeout time.Duration) (*WSConn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid tail URL: %w", err)
	}
	secure := false
	switch u.Scheme {
	case "wss", "https":
		secure = true
	case "ws", "http":
	default:
		return nil, fmt.Errorf("unsupported WebSocket scheme %q", u.Scheme)
	}
	host := u.Host
	if u.Port() == "" {
		if secure {
			host += ":443"
		} else {
			host += ":80"
		}
	}
	if handshakeTimeout <= 0 {
		handshakeTimeout = 30 * time.Second
	}
	dctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(dctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	if secure {
		tc := tls.Client(conn, &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(dctx); err != nil {
			conn.Close()
			return nil, err
		}
		conn = tc
	}
	if dl, ok := dctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}

	keyb := make([]byte, 16)
	rand.Read(keyb)
	key := base64.StdEncoding.EncodeToString(keyb)
	path := u.RequestURI()
	var b strings.Builder
	fmt.Fprintf(&b, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n", path, u.Host, key)
	if protocol != "" {
		fmt.Fprintf(&b, "Sec-WebSocket-Protocol: %s\r\n", protocol)
	}
	for k, vs := range header {
		for _, v := range vs {
			fmt.Fprintf(&b, "%s: %s\r\n", k, v)
		}
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(conn, b.String()); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("websocket handshake: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		conn.Close()
		return nil, fmt.Errorf("websocket handshake: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	if resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		conn.Close()
		return nil, fmt.Errorf("websocket handshake: bad Sec-WebSocket-Accept")
	}
	conn.SetDeadline(time.Time{})
	return &WSConn{conn: conn, br: br, Protocol: resp.Header.Get("Sec-WebSocket-Protocol")}, nil
}

// Opcodes.
const (
	opCont   = 0x0
	opText   = 0x1
	opBinary = 0x2
	opClose  = 0x8
	opPing   = 0x9
	opPong   = 0xA
)

// MaxWSMessage bounds a reassembled message.
const MaxWSMessage = 64 << 20

// ReadMessage returns the next text or binary message, answering pings and
// close frames along the way.
func (c *WSConn) ReadMessage() ([]byte, error) {
	var msg []byte
	for {
		fin, op, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch op {
		case opPing:
			c.write(opPong, payload)
			continue
		case opPong:
			continue
		case opClose:
			c.write(opClose, payload)
			return nil, ErrWSClosed
		case opText, opBinary, opCont:
			msg = append(msg, payload...)
			if len(msg) > MaxWSMessage {
				return nil, fmt.Errorf("websocket message too large")
			}
			if fin {
				return msg, nil
			}
		default:
			return nil, fmt.Errorf("websocket: unknown opcode %d", op)
		}
	}
}

func (c *WSConn) readFrame() (fin bool, op byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(c.br, h[:]); err != nil {
		return
	}
	fin = h[0]&0x80 != 0
	op = h[0] & 0x0f
	masked := h[1]&0x80 != 0
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err = io.ReadFull(c.br, b[:]); err != nil {
			return
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err = io.ReadFull(c.br, b[:]); err != nil {
			return
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if n > MaxWSMessage {
		err = fmt.Errorf("websocket frame too large")
		return
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(c.br, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return
}

// write sends one masked frame (clients must mask).
func (c *WSConn) write(op byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	buf := []byte{0x80 | op}
	n := len(payload)
	switch {
	case n < 126:
		buf = append(buf, 0x80|byte(n))
	case n < 1<<16:
		buf = append(buf, 0x80|126, byte(n>>8), byte(n))
	default:
		buf = append(buf, 0x80|127)
		buf = binary.BigEndian.AppendUint64(buf, uint64(n))
	}
	var mask [4]byte
	rand.Read(mask[:])
	buf = append(buf, mask[:]...)
	start := len(buf)
	buf = append(buf, payload...)
	for i := start; i < len(buf); i++ {
		buf[i] ^= mask[(i-start)%4]
	}
	c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := c.conn.Write(buf)
	return err
}

// WriteText sends a text message.
func (c *WSConn) WriteText(b []byte) error { return c.write(opText, b) }

// Ping sends a ping frame.
func (c *WSConn) Ping() error { return c.write(opPing, nil) }

// Close sends a close frame and closes the connection.
func (c *WSConn) Close() error {
	_ = c.write(opClose, []byte{0x03, 0xe8}) // 1000 normal closure
	return c.conn.Close()
}
