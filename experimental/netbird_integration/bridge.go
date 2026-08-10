//go:build with_netbird

package netbird_integration

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// BridgeTCPInfo describes an active TCP bridge.
type BridgeTCPInfo struct {
	Port   int
	Target string
	Active bool
}

// bridgeListener holds a running TCP bridge: it listens on a port inside
// the netbird overlay (netstack) and forwards every accepted connection
// to a local target (e.g. 127.0.0.1:8022 for Termux sshd).
//
// This is the missing "inbound" half of the userspace-netstack data path:
// netstack accepts TCP handshakes but has no way to deliver connections to
// processes listening on the host kernel stack. Bridging explicitly wires
// an overlay port to a host port. Verified on-device (2026-08-10): a peer
// connection from mipad to a probe listener was accepted and data flowed.
type bridgeListener struct {
	port     int
	target   string
	listener net.Listener
	done     chan struct{}
	wg       sync.WaitGroup
}

var (
	bridgeMu        sync.Mutex
	bridgeListeners = make(map[int]*bridgeListener)
)

// BridgeTCP listens on the given port inside the netbird overlay and
// forwards every accepted TCP connection to target (host:port).
// Idempotent per port: calling twice with the same port returns the
// existing bridge. Returns an error if the netbird engine is not running.
func BridgeTCP(port int, target string) (*BridgeTCPInfo, error) {
	client := GetClient()
	if client == nil {
		return nil, fmt.Errorf("netbird client not started")
	}

	bridgeMu.Lock()
	if bl, ok := bridgeListeners[port]; ok {
		bridgeMu.Unlock()
		return &BridgeTCPInfo{Port: bl.port, Target: bl.target, Active: true}, nil
	}
	bridgeMu.Unlock()

	listener, err := client.ListenTCP(net.JoinHostPort("", strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("netbird listen tcp :%d: %w", port, err)
	}

	bl := &bridgeListener{
		port:     port,
		target:   target,
		listener: listener,
		done:     make(chan struct{}),
	}
	bl.wg.Add(1)
	go bl.acceptLoop()

	bridgeMu.Lock()
	bridgeListeners[port] = bl
	bridgeMu.Unlock()

	log.Infof("netbird bridge: listening on overlay :%d → %s", port, target)
	return &BridgeTCPInfo{Port: port, Target: target, Active: true}, nil
}

// StopBridge stops the bridge for the given overlay port.
func StopBridge(port int) error {
	bridgeMu.Lock()
	bl, ok := bridgeListeners[port]
	if ok {
		delete(bridgeListeners, port)
	}
	bridgeMu.Unlock()
	if !ok {
		return nil
	}
	close(bl.done)
	_ = bl.listener.Close()
	bl.wg.Wait()
	log.Infof("netbird bridge: stopped overlay :%d", port)
	return nil
}

// StopAllBridges stops every active bridge.
func StopAllBridges() {
	bridgeMu.Lock()
	ports := make([]int, 0, len(bridgeListeners))
	for p := range bridgeListeners {
		ports = append(ports, p)
	}
	bridgeMu.Unlock()
	for _, p := range ports {
		_ = StopBridge(p)
	}
}

func (bl *bridgeListener) acceptLoop() {
	defer bl.wg.Done()
	for {
		conn, err := bl.listener.Accept()
		if err != nil {
			select {
			case <-bl.done:
				return
			default:
			}
			log.Warnf("netbird bridge :%d accept: %v", bl.port, err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		bl.wg.Add(1)
		go bl.handleConn(conn)
	}
}

func (bl *bridgeListener) handleConn(conn net.Conn) {
	defer bl.wg.Done()
	defer conn.Close()

	upstream, err := net.DialTimeout("tcp", bl.target, 10*time.Second)
	if err != nil {
		log.Warnf("netbird bridge :%d → %s dial: %v", bl.port, bl.target, err)
		return
	}
	defer upstream.Close()

	doneCh := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, conn)
		doneCh <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, upstream)
		doneCh <- struct{}{}
	}()

	// Exit when either direction finishes (half-close propagates via
	// CloseWrite below), or the bridge is stopped.
	select {
	case <-doneCh:
	case <-bl.done:
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}
	if utcp, ok := upstream.(*net.TCPConn); ok {
		_ = utcp.CloseWrite()
	}
	// Give the other direction a moment to flush, then tear down.
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
	}
}
