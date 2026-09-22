package ztp

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// Config starts one engine.
type Config struct {
	PrivateKey string     // this end's base64 private key
	ListenPort int        // UDP; 0 lets the system choose (tests)
	Address    netip.Addr // this end's tunnel address
	MTU        int        // 0 means 1420, WireGuard's usual
}

// Peer is one other end.
type Peer struct {
	PublicKey string
	// Allowed is what it may send from, and what is sent to it: a device's own
	// /32, or the enrolment /24 for the shared key.
	Allowed []netip.Prefix
	// Endpoint is where to reach it. Empty for a device, which calls in and is
	// answered wherever it called from.
	Endpoint  netip.AddrPort
	Keepalive int // seconds; 0 is off
}

// PeerStatus is what the engine reports about a peer.
type PeerStatus struct {
	PublicKey     string    `json:"publicKey"`
	Endpoint      string    `json:"endpoint"`
	LastHandshake time.Time `json:"lastHandshake"`
	RxBytes       int64     `json:"rxBytes"`
	TxBytes       int64     `json:"txBytes"`
}

// Engine is a userspace WireGuard device with its own IP stack.
type Engine struct {
	dev  *device.Device
	tnet *netstack.Net
	addr netip.Addr
}

// Start brings the device up and listens on the UDP port.
func Start(cfg Config) (*Engine, error) {
	if !cfg.Address.IsValid() {
		return nil, errors.New("the engine needs a tunnel address")
	}
	priv, err := hexKey(cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("private key: %w", err)
	}
	mtu := cfg.MTU
	if mtu == 0 {
		mtu = 1420
	}
	tun, tnet, err := netstack.CreateNetTUN([]netip.Addr{cfg.Address}, nil, mtu)
	if err != nil {
		return nil, err
	}
	dev := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelError, "[ztp] "))
	if err := dev.IpcSet(fmt.Sprintf("private_key=%s\nlisten_port=%d\n", priv, cfg.ListenPort)); err != nil {
		dev.Close()
		return nil, err
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, err
	}
	return &Engine{dev: dev, tnet: tnet, addr: cfg.Address}, nil
}

// Address is this end's tunnel address.
func (e *Engine) Address() netip.Addr { return e.addr }

// SetPeer adds a peer or replaces its settings, allowed addresses included.
func (e *Engine) SetPeer(p Peer) error {
	pub, err := hexKey(p.PublicKey)
	if err != nil {
		return fmt.Errorf("peer key: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "public_key=%s\nreplace_allowed_ips=true\n", pub)
	for _, a := range p.Allowed {
		fmt.Fprintf(&b, "allowed_ip=%s\n", a.String())
	}
	if p.Endpoint.IsValid() {
		fmt.Fprintf(&b, "endpoint=%s\n", p.Endpoint.String())
	}
	fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", p.Keepalive)
	return e.dev.IpcSet(b.String())
}

// RemovePeer removes a peer. Removing one that is not there is not an error.
func (e *Engine) RemovePeer(publicKey string) error {
	pub, err := hexKey(publicKey)
	if err != nil {
		return fmt.Errorf("peer key: %w", err)
	}
	return e.dev.IpcSet(fmt.Sprintf("public_key=%s\nremove=true\n", pub))
}

// DialContext connects through the tunnel: how MikroDash reaches a remote
// router's API.
func (e *Engine) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return e.tnet.DialContext(ctx, network, addr)
}

// ListenTCP listens on this end's tunnel address.
func (e *Engine) ListenTCP(port int) (net.Listener, error) {
	return e.tnet.ListenTCP(&net.TCPAddr{IP: net.IP(e.addr.AsSlice()), Port: port})
}

// Status reports every peer, from the device's own configuration dump.
func (e *Engine) Status() ([]PeerStatus, error) {
	dump, err := e.dev.IpcGet()
	if err != nil {
		return nil, err
	}
	return parseStatus(dump), nil
}

// ListenPort is the UDP port the device listens on: the configured one, or the
// one the system chose for 0.
func (e *Engine) ListenPort() (int, error) {
	dump, err := e.dev.IpcGet()
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(dump, "\n") {
		if v, ok := strings.CutPrefix(line, "listen_port="); ok {
			return strconv.Atoi(v)
		}
	}
	return 0, errors.New("the device reported no listen port")
}

// parseStatus reads wireguard-go's `get=1` dump: key=value lines, a peer
// starting at each public_key. Pure, so it is tested on text.
func parseStatus(dump string) []PeerStatus {
	out := []PeerStatus{}
	var cur *PeerStatus
	var sec, nsec int64
	flush := func() {
		if cur != nil {
			if sec > 0 {
				cur.LastHandshake = time.Unix(sec, nsec).UTC()
			}
			out = append(out, *cur)
		}
	}
	sc := bufio.NewScanner(strings.NewReader(dump))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "public_key":
			flush()
			cur, sec, nsec = &PeerStatus{}, 0, 0
			if b, err := hex.DecodeString(v); err == nil {
				cur.PublicKey = base64.StdEncoding.EncodeToString(b)
			}
		case "endpoint":
			if cur != nil {
				cur.Endpoint = v
			}
		case "last_handshake_time_sec":
			sec, _ = strconv.ParseInt(v, 10, 64)
		case "last_handshake_time_nsec":
			nsec, _ = strconv.ParseInt(v, 10, 64)
		case "rx_bytes":
			if cur != nil {
				cur.RxBytes, _ = strconv.ParseInt(v, 10, 64)
			}
		case "tx_bytes":
			if cur != nil {
				cur.TxBytes, _ = strconv.ParseInt(v, 10, 64)
			}
		}
	}
	flush()
	return out
}

// Close takes the device down and releases the UDP port.
func (e *Engine) Close() { e.dev.Close() }
