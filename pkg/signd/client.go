package signd

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

// Client talks to a bsgo-signd server over TCP, optionally with mTLS. Each
// call uses its own connection.
type Client struct {
	addr      string
	tlsConfig *tls.Config
}

func NewClient(addr string) *Client {
	return &Client{addr: addr}
}

func NewTLSClient(addr string, tlsConfig *tls.Config) *Client {
	return &Client{addr: addr, tlsConfig: tlsConfig}
}

func (c *Client) SetTLSConfig(cfg *tls.Config) {
	c.tlsConfig = cfg
}

func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	if c.tlsConfig != nil {
		tlsDialer := &tls.Dialer{NetDialer: &d, Config: c.tlsConfig}
		return tlsDialer.DialContext(ctx, "tcp", c.addr)
	}
	return d.DialContext(ctx, "tcp", c.addr)
}

// call sends one command line and returns the fields after "OK".
func (c *Client) call(ctx context.Context, line string) ([]string, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("connecting to bsgo-signd at %s: %w", c.addr, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(dl); err != nil {
			return nil, err
		}
	}
	if _, err := io.WriteString(conn, line+"\n"); err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return nil, err
		}
		return nil, errEmptyResponse
	}
	resp := strings.TrimSpace(sc.Text())
	rest, ok := strings.CutPrefix(resp, "OK")
	if !ok {
		return nil, fmt.Errorf("signd rejected request: %s", resp)
	}
	return strings.Fields(rest), nil
}

// GenerateKey asks signd to create an OpenPGP key. days <= 0 selects the
// server default lifetime.
func (c *Client) GenerateKey(ctx context.Context, project, flavor string, days int) (*KeyMaterial, error) {
	if flavor == "" {
		flavor = "default"
	}
	fields, err := c.call(ctx, fmt.Sprintf("GENKEY %s %s %d", project, flavor, days))
	if err != nil {
		return nil, err
	}
	return parseMaterial(fields)
}

// PublicKey derives the public key material from an encrypted private key.
func (c *Client) PublicKey(ctx context.Context, encryptedKey string) (*KeyMaterial, error) {
	fields, err := c.call(ctx, "PUBKEY "+encryptedKey)
	if err != nil {
		return nil, err
	}
	return parseMaterial(fields)
}

// ExtendKey makes the key expire days from now and returns the updated key.
func (c *Client) ExtendKey(ctx context.Context, encryptedKey string, days int) (*KeyMaterial, error) {
	fields, err := c.call(ctx, fmt.Sprintf("EXTEND %s %d", encryptedKey, days))
	if err != nil {
		return nil, err
	}
	return parseMaterial(fields)
}

func hexResult(fields []string, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	if len(fields) != 1 {
		return nil, fmt.Errorf("unexpected response with %d fields", len(fields))
	}
	return hex.DecodeString(fields[0])
}

// SignatureTrailer returns the bytes to hash after the document.
func (c *Client) SignatureTrailer(ctx context.Context, encryptedKey string, p SignParams) ([]byte, error) {
	line := fmt.Sprintf("TRAILER %s %s %d %d", encryptedKey, p.Hash, p.SigType, p.Created.Unix())
	return hexResult(c.call(ctx, line))
}

// SignDigest returns an OpenPGP signature packet (binary or armored) over
// digest = H(document || trailer).
func (c *Client) SignDigest(ctx context.Context, encryptedKey string, p SignParams, digest []byte, armored bool) ([]byte, error) {
	format := "binary"
	if armored {
		format = "armor"
	}
	line := fmt.Sprintf("SIGN %s %s %s %d %s %s", encryptedKey, p.Hash, hex.EncodeToString(digest),
		p.SigType, strconv.FormatInt(p.Created.Unix(), 10), format)
	return hexResult(c.call(ctx, line))
}

// SignRaw returns a raw RSA PKCS#1 v1.5 signature over digest = H(message).
func (c *Client) SignRaw(ctx context.Context, encryptedKey, hashAlgo string, digest []byte) ([]byte, error) {
	return hexResult(c.call(ctx, fmt.Sprintf("RAWSIGN %s %s %s", encryptedKey, hashAlgo, hex.EncodeToString(digest))))
}
