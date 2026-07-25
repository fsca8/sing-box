// experimental/monitor/timingconn.go
package monitor

import (
	"net"
	"sync/atomic"
	"time"
)

// TimingConn wraps a net.Conn to measure TLS handshake time for direct connections.
//
// How it works:
//   - Records the time of the first Write call (≈ ClientHello sent to remote server)
//   - Records the time of the first Read call that returns data (≈ ServerHello response received)
//   - The difference approximates the initial TLS handshake round-trip (≈ 1 RTT)
//
// NOTE: This does NOT measure the full TLS handshake time (which includes certificate
// verification, key exchange, and Finished messages, typically 2-3 RTTs total).
// However, it provides a consistent relative metric for comparing TLS performance
// across different connections on the same network path.
type TimingConn struct {
	net.Conn
	firstWrite  atomic.Int64 // UnixNano of first Write call
	firstRead   atomic.Int64 // UnixNano of first Read that returns data
	recorded    atomic.Bool  // ensure we only record once
	remoteAddr  string
	connID      string
	domain      string
	outbound    string
}

// WrapWithTiming wraps a connection for TLS handshake timing.
// Only used for direct outbound connections where sing-box doesn't do TLS.
func WrapWithTiming(conn net.Conn, connID, domain, outbound string) *TimingConn {
	return &TimingConn{
		Conn:       conn,
		remoteAddr: conn.RemoteAddr().String(),
		connID:     connID,
		domain:     domain,
		outbound:   outbound,
	}
}

func (c *TimingConn) Write(b []byte) (int, error) {
	if c.firstWrite.Load() == 0 {
		c.firstWrite.Store(time.Now().UnixNano())
	}
	return c.Conn.Write(b)
}

func (c *TimingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if err == nil && n > 0 && c.firstRead.Load() == 0 {
		now := time.Now().UnixNano()
		c.firstRead.Store(now)
		// Record TLS timing once: time from first write to first read
		go c.recordTLS()
	}
	return n, err
}

func (c *TimingConn) recordTLS() {
	if !c.recorded.CompareAndSwap(false, true) {
		return
	}
	firstWrite := c.firstWrite.Load()
	firstRead := c.firstRead.Load()
	if firstWrite == 0 || firstRead == 0 {
		return
	}
	tlsUs := (firstRead - firstWrite) / 1000 // nanoseconds to microseconds
	if tlsUs <= 0 {
		return
	}
	mc := Get()
	if mc == nil {
		return
	}
	mc.RecordTLS(TLSRecord{
		Remote:     c.remoteAddr,
		ConnID:     c.connID,
		Domain:     c.domain,
		Outbound:   c.outbound,
		LatencyUs:  tlsUs,
	})
}
