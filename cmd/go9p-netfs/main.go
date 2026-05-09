// netfs is a go9p synthetic filesystem that models a subset of Plan 9's /net device
// interfaces (ip(3), ether(3), bridge(3)) on top of Linux userland networking.
//
// Scope / philosophy
//
// Plan 9's /net is a kernel device that exposes an entire networking stack as a
// filesystem. Implementing all of ip(3)/ether(3)/bridge(3) faithfully would
// require deep integration with routing, ARP/NDP, raw packet I/O, and netlink.
//
// This example focuses on:
//   - building the *shape* of the filesystem (directories/files and clone patterns),
//   - implementing a working and testable TCP conversation interface (clone/ctl/data),
//   - providing minimal, mostly read-only stubs for the rest, suitable as a starting
//     point for further expansion.
//
// Implemented today:
//   - /net/tcp: clone, per-conversation ctl/connect/close, data stream, local/remote/status
//   - /net/ndb: read/write small config blob (1024 bytes) like ip(3) describes
//   - /net/ipifc: read-only "status" and "stats" views based on net.Interfaces()
//   - /net/ether0: addr plus clone + per-connection ctl/type/data (data is stub)
//   - /net/bridge0: ctl/cache/log/stats (mostly stub, log is a simple append buffer)
//
// Not yet implemented (placeholders exist):
//   - full ipifc control messages (bind/add/remove routes, ARP/NDP admin, etc.)
//   - udp/rudp/icmp/gre/esp/ipmux protocol stacks
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lionkov/go9p/p"
	"github.com/lionkov/go9p/p/srv"
)

var (
	addr  = flag.String("addr", ":5640", "network address")
	debug = flag.Bool("d", false, "print debug messages")
)

type NetFS struct {
	srv *srv.Fsrv

	mu      sync.Mutex
	nextTCP int
	nextIfc int
	nextEth int

	root *srv.File
	net  *srv.File

	// Protocol dirs (ip(3) style).
	tcp    *srv.File
	udp    *srv.File
	icmp   *srv.File
	icmpv6 *srv.File
	gre    *srv.File
	esp    *srv.File
	ipmux  *srv.File
	rudp   *srv.File

	// ipifc (ip(3) interface configuration).
	ipifc      *srv.File
	ipifcNDB   *NDBFile
	ipifcStats *IPIFCStatsFile

	arp       *ARPFile
	iproute   *IPRouteFile
	ipselftab *IPSelftabFile
	netlog    *NetLogFile

	// ether(3) style (single device: ether0).
	ether0 *srv.File

	// bridge(3) style (single bridge: bridge0).
	bridge0 *srv.File
}

// ---------- common helpers ----------

func parseConnectArg(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("missing address")
	}
	// Accept "host!port" as Plan 9-ish spelling.
	if strings.Count(s, "!") == 1 && !strings.Contains(s, " ") {
		parts := strings.SplitN(s, "!", 2)
		if parts[0] == "" || parts[1] == "" {
			return "", fmt.Errorf("invalid address %q", s)
		}
		return net.JoinHostPort(parts[0], parts[1]), nil
	}
	// Accept "host:port" as a single token.
	if strings.Count(s, ":") >= 1 && !strings.Contains(s, " ") {
		_, _, err := net.SplitHostPort(s)
		if err != nil {
			return "", err
		}
		return s, nil
	}
	// Accept "host port" (two tokens).
	fields := strings.Fields(s)
	if len(fields) == 2 {
		return net.JoinHostPort(fields[0], fields[1]), nil
	}
	return "", fmt.Errorf("expected host!port, host:port, or host port; got %q", s)
}

func readWithOffset(b []byte, buf []byte, offset uint64) (int, error) {
	if offset >= uint64(len(b)) {
		return 0, nil
	}
	b = b[offset:]
	if len(b) > len(buf) {
		b = b[:len(buf)]
	}
	copy(buf, b)
	return len(b), nil
}

// ---------- generic protocol dir stubs (ip(3)) ----------

type ProtoCloneStub struct{ srv.File }

func (f *ProtoCloneStub) Read(fid *srv.FFid, buf []byte, offset uint64) (int, error) {
	return 0, &p.Error{Err: "protocol clone not implemented (netfs example)", Errornum: p.EIO}
}

// ---------- /net/log (ip(3) minimal) ----------

type NetLogFile struct {
	srv.File
	mu   sync.Mutex
	data []byte
	only string
}

func (f *NetLogFile) Read(fid *srv.FFid, buf []byte, offset uint64) (int, error) {
	f.mu.Lock()
	b := append([]byte(nil), f.data...)
	f.mu.Unlock()
	return readWithOffset(b, buf, offset)
}

func (f *NetLogFile) Write(fid *srv.FFid, data []byte, offset uint64) (int, error) {
	// Accept ip(3) style "set ...", "clear ...", "only addr" messages and record them.
	cmd := strings.TrimSpace(string(data))
	if cmd == "" {
		return len(data), nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data = append(f.data, []byte(cmd+"\n")...)
	if strings.HasPrefix(cmd, "only ") {
		f.only = strings.TrimSpace(strings.TrimPrefix(cmd, "only"))
	}
	if len(f.data) > 256*1024 {
		f.data = f.data[len(f.data)-128*1024:]
	}
	return len(data), nil
}

// ---------- /net/arp (ip(3) minimal) ----------

type ARPFile struct{ srv.File }

func (f *ARPFile) Read(fid *srv.FFid, buf []byte, offset uint64) (int, error) {
	// We don't maintain an ARP cache; return empty.
	return 0, nil
}

func (f *ARPFile) Write(fid *srv.FFid, data []byte, offset uint64) (int, error) {
	// Accept and ignore administrative commands (flush/add/del).
	return len(data), nil
}

// ---------- /net/iproute (ip(3) minimal) ----------

type IPRouteFile struct{ srv.File }

func (f *IPRouteFile) Read(fid *srv.FFid, buf []byte, offset uint64) (int, error) {
	// Route inspection requires netlink; leave empty for the example.
	return 0, nil
}

func (f *IPRouteFile) Write(fid *srv.FFid, data []byte, offset uint64) (int, error) {
	// Accept and ignore route commands (flush/tag/add/remove/route ...).
	return len(data), nil
}

// ---------- /net/ipselftab (ip(3) minimal) ----------

type IPSelftabFile struct{ srv.File }

func (f *IPSelftabFile) Read(fid *srv.FFid, buf []byte, offset uint64) (int, error) {
	// Enumerate local interface addresses.
	ifis, _ := net.Interfaces()
	seen := map[string]int{}
	for _, ifi := range ifis {
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			s := a.String()
			seen[s]++
		}
	}
	lines := make([]string, 0, len(seen))
	for a, n := range seen {
		// flags: keep empty; ip(3) uses route flags but we don't compute them here.
		lines = append(lines, fmt.Sprintf("%s %d -", a, n))
	}
	out := strings.Join(lines, "\n") + "\n"
	return readWithOffset([]byte(out), buf, offset)
}

// ---------- /net/tcp (ip(3) subset) ----------

type TCPClone struct {
	srv.File
	nfs *NetFS
}

type TCPConv struct {
	id int

	mu       sync.Mutex
	conn     net.Conn
	state    string
	lastErr  string
	created  time.Time
	peerAddr string
}

type TCPCTL struct {
	srv.File
	conv *TCPConv
}

type TCPData struct {
	srv.File
	conv *TCPConv
}

type TCPStatus struct {
	srv.File
	conv *TCPConv
}

type TCPError struct {
	srv.File
	conv *TCPConv
}

type TCPStringFile struct {
	srv.File
	conv *TCPConv
	kind string // "local" | "remote"
}

func (c *TCPConv) snapshot() (state, local, remote, lastErr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state = c.state
	lastErr = c.lastErr
	if c.conn != nil {
		local = c.conn.LocalAddr().String()
		remote = c.conn.RemoteAddr().String()
	}
	if remote == "" {
		remote = c.peerAddr
	}
	return state, local, remote, lastErr
}

func (c *TCPConv) connect(hostport string) error {
	c.mu.Lock()
	if c.conn != nil {
		c.mu.Unlock()
		return fmt.Errorf("already connected")
	}
	c.state = "connecting"
	c.peerAddr = hostport
	c.lastErr = ""
	c.mu.Unlock()

	conn, err := net.Dial("tcp", hostport)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.state = "error"
		c.lastErr = err.Error()
		return err
	}
	c.conn = conn
	c.state = "connected"
	c.peerAddr = conn.RemoteAddr().String()
	return nil
}

func (c *TCPConv) close() error {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.state = "closed"
	c.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

func (f *TCPClone) Read(fid *srv.FFid, buf []byte, offset uint64) (int, error) {
	if offset > 0 {
		return 0, nil
	}

	f.nfs.mu.Lock()
	f.nfs.nextTCP++
	id := f.nfs.nextTCP
	f.nfs.mu.Unlock()

	user := p.OsUsers.Uid2User(os.Geteuid())
	conv := &TCPConv{id: id, state: "new", created: time.Now()}

	// /net/tcp/<id> directory.
	dir := new(srv.File)
	if err := dir.Add(f.nfs.tcp, strconv.Itoa(id), user, nil, p.DMDIR|0555, nil); err != nil {
		return 0, err
	}

	ctl := new(TCPCTL)
	ctl.conv = conv
	if err := ctl.Add(dir, "ctl", user, nil, 0o666, ctl); err != nil {
		return 0, err
	}
	data := new(TCPData)
	data.conv = conv
	if err := data.Add(dir, "data", user, nil, 0o666, data); err != nil {
		return 0, err
	}
	st := new(TCPStatus)
	st.conv = conv
	if err := st.Add(dir, "status", user, nil, 0o444, st); err != nil {
		return 0, err
	}
	local := new(TCPStringFile)
	local.conv = conv
	local.kind = "local"
	if err := local.Add(dir, "local", user, nil, 0o444, local); err != nil {
		return 0, err
	}
	remote := new(TCPStringFile)
	remote.conv = conv
	remote.kind = "remote"
	if err := remote.Add(dir, "remote", user, nil, 0o444, remote); err != nil {
		return 0, err
	}

	ef := new(TCPError)
	ef.conv = conv
	if err := ef.Add(dir, "error", user, nil, 0o444, ef); err != nil {
		return 0, err
	}

	out := []byte(strconv.Itoa(id) + "\n")
	if len(out) > len(buf) {
		out = out[:len(buf)]
	}
	copy(buf, out)
	return len(out), nil
}

func (f *TCPCTL) Write(fid *srv.FFid, data []byte, offset uint64) (int, error) {
	cmd := strings.TrimSpace(string(data))
	if cmd == "" {
		return len(data), nil
	}
	fields := strings.Fields(cmd)
	switch fields[0] {
	case "connect":
		arg := strings.TrimSpace(strings.TrimPrefix(cmd, "connect"))
		hp, err := parseConnectArg(arg)
		if err != nil {
			f.conv.mu.Lock()
			f.conv.lastErr = err.Error()
			f.conv.mu.Unlock()
			return 0, err
		}
		if err := f.conv.connect(hp); err != nil {
			// connect() already sets lastErr on failure, but keep it explicit for ctl users.
			f.conv.mu.Lock()
			f.conv.lastErr = err.Error()
			f.conv.mu.Unlock()
			return 0, err
		}
		f.conv.mu.Lock()
		f.conv.lastErr = ""
		f.conv.mu.Unlock()
		return len(data), nil
	case "close":
		_ = f.conv.close()
		f.conv.mu.Lock()
		f.conv.lastErr = ""
		f.conv.mu.Unlock()
		return len(data), nil
	default:
		err := fmt.Errorf("unknown ctl command %q", fields[0])
		f.conv.mu.Lock()
		f.conv.lastErr = err.Error()
		f.conv.mu.Unlock()
		return 0, err
	}
}

func (f *TCPError) Read(fid *srv.FFid, buf []byte, offset uint64) (int, error) {
	_, _, _, lastErr := f.conv.snapshot()
	if strings.TrimSpace(lastErr) == "" {
		lastErr = "ok"
	}
	lastErr += "\n"
	return readWithOffset([]byte(lastErr), buf, offset)
}

func (f *TCPData) Read(fid *srv.FFid, buf []byte, offset uint64) (int, error) {
	f.conv.mu.Lock()
	conn := f.conv.conn
	f.conv.mu.Unlock()
	if conn == nil {
		return 0, &p.Error{Err: "not connected", Errornum: p.EIO}
	}
	n, err := conn.Read(buf)
	if err != nil {
		if err == io.EOF {
			return 0, nil
		}
		return n, err
	}
	return n, nil
}

func (f *TCPData) Write(fid *srv.FFid, data []byte, offset uint64) (int, error) {
	f.conv.mu.Lock()
	conn := f.conv.conn
	f.conv.mu.Unlock()
	if conn == nil {
		return 0, &p.Error{Err: "not connected", Errornum: p.EIO}
	}
	return conn.Write(data)
}

func (f *TCPStatus) Read(fid *srv.FFid, buf []byte, offset uint64) (int, error) {
	state, local, remote, lastErr := f.conv.snapshot()
	line := fmt.Sprintf("id=%d state=%s local=%s remote=%s err=%s\n", f.conv.id, state, local, remote, lastErr)
	return readWithOffset([]byte(line), buf, offset)
}

func (f *TCPStringFile) Read(fid *srv.FFid, buf []byte, offset uint64) (int, error) {
	_, local, remote, _ := f.conv.snapshot()
	val := ""
	switch f.kind {
	case "local":
		val = local
	case "remote":
		val = remote
	default:
		val = ""
	}
	if val == "" {
		val = "-"
	}
	val += "\n"
	return readWithOffset([]byte(val), buf, offset)
}

// ---------- /net/ndb (ip(3)) ----------

type NDBFile struct {
	srv.File
	mu   sync.Mutex
	data []byte
}

func (f *NDBFile) Read(fid *srv.FFid, buf []byte, offset uint64) (int, error) {
	f.mu.Lock()
	b := append([]byte(nil), f.data...)
	f.mu.Unlock()
	return readWithOffset(b, buf, offset)
}

func (f *NDBFile) Write(fid *srv.FFid, data []byte, offset uint64) (int, error) {
	// Model ip(3)'s note that /net/ndb may contain up to 1024 bytes.
	f.mu.Lock()
	defer f.mu.Unlock()
	if int(offset) > len(f.data) {
		// Zero-fill gap.
		gap := make([]byte, int(offset)-len(f.data))
		f.data = append(f.data, gap...)
	}
	end := int(offset) + len(data)
	if end > 1024 {
		return 0, &p.Error{Err: "ndb: too large (max 1024 bytes)", Errornum: p.EIO}
	}
	if end > len(f.data) {
		n := make([]byte, end)
		copy(n, f.data)
		f.data = n
	}
	copy(f.data[offset:], data)
	return len(data), nil
}

// ---------- /net/ipifc (ip(3) minimal read-only view) ----------

type IPIFCStatsFile struct{ srv.File }

func (f *IPIFCStatsFile) Read(fid *srv.FFid, buf []byte, offset uint64) (int, error) {
	// We don't implement real kernel stats; provide a tagged stub (ip(3) describes tagged fields).
	out := "ipifc: stub (netfs example)\n"
	return readWithOffset([]byte(out), buf, offset)
}

type IPIFCStatusFile struct {
	srv.File
	ifName string
}

func (f *IPIFCStatusFile) Read(fid *srv.FFid, buf []byte, offset uint64) (int, error) {
	ifi, err := net.InterfaceByName(f.ifName)
	if err != nil {
		return 0, err
	}
	addrs, _ := ifi.Addrs()
	lines := make([]string, 0, len(addrs)+1)
	// ip(3) says: first line includes device, mtu, local, mask, remote/network, pkt in/out, errs...
	// We don't have packet counters here; emit placeholders.
	mtu := ifi.MTU
	dev := ifi.Name
	if len(addrs) == 0 {
		lines = append(lines, fmt.Sprintf("%s %d - - - 0 0 0 0", dev, mtu))
	} else {
		for i, a := range addrs {
			local := a.String()
			mask := "-"
			remote := "-"
			line := ""
			if i == 0 {
				line = fmt.Sprintf("%s %d %s %s %s 0 0 0 0", dev, mtu, local, mask, remote)
			} else {
				line = fmt.Sprintf("%s %s %s 0 0 0 0", local, mask, remote)
			}
			lines = append(lines, line)
		}
	}
	out := strings.Join(lines, "\n") + "\n"
	return readWithOffset([]byte(out), buf, offset)
}

// ---------- /net/ether0 (ether(3) minimal) ----------

type EtherAddrFile struct{ srv.File }

func (f *EtherAddrFile) Read(fid *srv.FFid, buf []byte, offset uint64) (int, error) {
	// Use the first interface with a MAC address as "ether0".
	ifis, _ := net.Interfaces()
	mac := ""
	for _, ifi := range ifis {
		if len(ifi.HardwareAddr) > 0 {
			mac = strings.ReplaceAll(ifi.HardwareAddr.String(), ":", "")
			break
		}
	}
	if mac == "" {
		mac = "000000000000"
	}
	return readWithOffset([]byte(mac), buf, offset) // no trailing newline per ether(3)
}

type EtherClone struct {
	srv.File
	nfs *NetFS
	dir *srv.File // /net/ether0
}

type EtherConn struct {
	mu          sync.Mutex
	etype       int
	promisc     bool
	headersonly bool
	lastErr     string
}

type EtherCtl struct {
	srv.File
	conn *EtherConn
}

type EtherErrorFile struct {
	srv.File
	conn *EtherConn
}

type EtherTypeFile struct {
	srv.File
	conn *EtherConn
}

type EtherData struct {
	srv.File
	conn *EtherConn
}

func (f *EtherClone) Read(fid *srv.FFid, buf []byte, offset uint64) (int, error) {
	if offset > 0 {
		return 0, nil
	}
	f.nfs.mu.Lock()
	f.nfs.nextEth++
	id := f.nfs.nextEth
	f.nfs.mu.Unlock()

	user := p.OsUsers.Uid2User(os.Geteuid())
	conn := &EtherConn{etype: -1}

	d := new(srv.File)
	if err := d.Add(f.dir, strconv.Itoa(id), user, nil, p.DMDIR|0555, nil); err != nil {
		return 0, err
	}

	ctl := new(EtherCtl)
	ctl.conn = conn
	if err := ctl.Add(d, "ctl", user, nil, 0o666, ctl); err != nil {
		return 0, err
	}
	typ := new(EtherTypeFile)
	typ.conn = conn
	if err := typ.Add(d, "type", user, nil, 0o444, typ); err != nil {
		return 0, err
	}
	data := new(EtherData)
	data.conn = conn
	if err := data.Add(d, "data", user, nil, 0o666, data); err != nil {
		return 0, err
	}

	ef := new(EtherErrorFile)
	ef.conn = conn
	if err := ef.Add(d, "error", user, nil, 0o444, ef); err != nil {
		return 0, err
	}

	out := []byte(strconv.Itoa(id) + "\n")
	if len(out) > len(buf) {
		out = out[:len(buf)]
	}
	copy(buf, out)
	return len(out), nil
}

func (f *EtherCtl) Write(fid *srv.FFid, data []byte, offset uint64) (int, error) {
	cmd := strings.TrimSpace(string(data))
	if cmd == "" {
		return len(data), nil
	}
	fields := strings.Fields(cmd)
	f.conn.mu.Lock()
	defer f.conn.mu.Unlock()
	switch fields[0] {
	case "connect":
		if len(fields) != 2 {
			f.conn.lastErr = "connect: expected type"
			return 0, fmt.Errorf("%s", f.conn.lastErr)
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil {
			f.conn.lastErr = err.Error()
			return 0, err
		}
		f.conn.etype = n
		f.conn.lastErr = ""
		return len(data), nil
	case "promiscuous":
		f.conn.promisc = true
		f.conn.lastErr = ""
		return len(data), nil
	case "headersonly":
		f.conn.headersonly = true
		f.conn.lastErr = ""
		return len(data), nil
	default:
		// Accept and ignore ip(3) interface control messages to match ether(3)'s note.
		f.conn.lastErr = ""
		return len(data), nil
	}
}

func (f *EtherErrorFile) Read(fid *srv.FFid, buf []byte, offset uint64) (int, error) {
	f.conn.mu.Lock()
	lastErr := f.conn.lastErr
	f.conn.mu.Unlock()
	if strings.TrimSpace(lastErr) == "" {
		lastErr = "ok"
	}
	lastErr += "\n"
	return readWithOffset([]byte(lastErr), buf, offset)
}

func (f *EtherTypeFile) Read(fid *srv.FFid, buf []byte, offset uint64) (int, error) {
	f.conn.mu.Lock()
	v := f.conn.etype
	f.conn.mu.Unlock()
	return readWithOffset([]byte(fmt.Sprintf("%d\n", v)), buf, offset)
}

func (f *EtherData) Read(fid *srv.FFid, buf []byte, offset uint64) (int, error) {
	return 0, &p.Error{Err: "ether data not implemented (netfs example)", Errornum: p.EIO}
}

func (f *EtherData) Write(fid *srv.FFid, data []byte, offset uint64) (int, error) {
	return 0, &p.Error{Err: "ether data not implemented (netfs example)", Errornum: p.EIO}
}

// ---------- /net/bridge0 (bridge(3) minimal) ----------

type BridgeLog struct {
	srv.File
	mu   sync.Mutex
	data []byte
}

func (f *BridgeLog) Read(fid *srv.FFid, buf []byte, offset uint64) (int, error) {
	// A real bridge log blocks waiting for new data; for the example, just return what's buffered.
	f.mu.Lock()
	b := append([]byte(nil), f.data...)
	f.mu.Unlock()
	return readWithOffset(b, buf, offset)
}

func (f *BridgeLog) appendLine(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data = append(f.data, []byte(s)...)
	if len(f.data) > 256*1024 {
		// prevent unbounded growth
		f.data = f.data[len(f.data)-128*1024:]
	}
}

type BridgeCtl struct {
	srv.File
	log *BridgeLog
}

func (f *BridgeCtl) Write(fid *srv.FFid, data []byte, offset uint64) (int, error) {
	cmd := strings.TrimSpace(string(data))
	if cmd == "" {
		return len(data), nil
	}
	// We accept control strings and log them, but do not actually bridge packets.
	f.log.appendLine(cmd + "\n")
	return len(data), nil
}

type ROTextFile struct {
	srv.File
	text string
}

func (f *ROTextFile) Read(fid *srv.FFid, buf []byte, offset uint64) (int, error) {
	return readWithOffset([]byte(f.text), buf, offset)
}

// ---------- build tree ----------

func buildNetFS() (*NetFS, error) {
	nfs := &NetFS{}
	user := p.OsUsers.Uid2User(os.Geteuid())

	nfs.root = new(srv.File)
	if err := nfs.root.Add(nil, "/", user, nil, p.DMDIR|0555, nil); err != nil {
		return nil, err
	}
	nfs.net = new(srv.File)
	if err := nfs.net.Add(nfs.root, "net", user, nil, p.DMDIR|0555, nil); err != nil {
		return nil, err
	}

	// /net/ndb
	nfs.ipifcNDB = new(NDBFile)
	if err := nfs.ipifcNDB.Add(nfs.net, "ndb", user, nil, 0o666, nfs.ipifcNDB); err != nil {
		return nil, err
	}

	// /net/log
	nfs.netlog = new(NetLogFile)
	if err := nfs.netlog.Add(nfs.net, "log", user, nil, 0o666, nfs.netlog); err != nil {
		return nil, err
	}

	// /net/arp, /net/iproute, /net/ipselftab
	nfs.arp = new(ARPFile)
	if err := nfs.arp.Add(nfs.net, "arp", user, nil, 0o666, nfs.arp); err != nil {
		return nil, err
	}
	nfs.iproute = new(IPRouteFile)
	if err := nfs.iproute.Add(nfs.net, "iproute", user, nil, 0o666, nfs.iproute); err != nil {
		return nil, err
	}
	nfs.ipselftab = new(IPSelftabFile)
	if err := nfs.ipselftab.Add(nfs.net, "ipselftab", user, nil, 0o444, nfs.ipselftab); err != nil {
		return nil, err
	}

	// /net/ipifc
	nfs.ipifc = new(srv.File)
	if err := nfs.ipifc.Add(nfs.net, "ipifc", user, nil, p.DMDIR|0555, nil); err != nil {
		return nil, err
	}
	nfs.ipifcStats = new(IPIFCStatsFile)
	if err := nfs.ipifcStats.Add(nfs.ipifc, "stats", user, nil, 0o444, nfs.ipifcStats); err != nil {
		return nil, err
	}

	// Expose host interfaces as numbered directories, each with a status file.
	ifis, _ := net.Interfaces()
	for i, ifi := range ifis {
		d := new(srv.File)
		if err := d.Add(nfs.ipifc, strconv.Itoa(i), user, nil, p.DMDIR|0555, nil); err != nil {
			return nil, err
		}
		st := new(IPIFCStatusFile)
		st.ifName = ifi.Name
		if err := st.Add(d, "status", user, nil, 0o444, st); err != nil {
			return nil, err
		}
		// ctl exists but is a stub today.
		ctl := &ROTextFile{text: "ipifc ctl: not implemented (netfs example)\n"}
		if err := ctl.Add(d, "ctl", user, nil, 0o666, ctl); err != nil {
			return nil, err
		}
	}

	// /net/tcp (working)
	nfs.tcp = new(srv.File)
	if err := nfs.tcp.Add(nfs.net, "tcp", user, nil, p.DMDIR|0555, nil); err != nil {
		return nil, err
	}
	tclone := new(TCPClone)
	tclone.nfs = nfs
	if err := tclone.Add(nfs.tcp, "clone", user, nil, 0o444, tclone); err != nil {
		return nil, err
	}
	tstats := &ROTextFile{text: "tcp stats: not implemented (netfs example)\n"}
	if err := tstats.Add(nfs.tcp, "stats", user, nil, 0o444, tstats); err != nil {
		return nil, err
	}

	// Other ip(3) protocol directories (placeholders).
	addProto := func(name string) (*srv.File, error) {
		d := new(srv.File)
		if err := d.Add(nfs.net, name, user, nil, p.DMDIR|0555, nil); err != nil {
			return nil, err
		}
		clone := new(ProtoCloneStub)
		if err := clone.Add(d, "clone", user, nil, 0o444, clone); err != nil {
			return nil, err
		}
		stats := &ROTextFile{text: name + " stats: not implemented (netfs example)\n"}
		if err := stats.Add(d, "stats", user, nil, 0o444, stats); err != nil {
			return nil, err
		}
		return d, nil
	}
	{
		var e error
		if nfs.udp, e = addProto("udp"); e != nil {
			return nil, e
		}
		if nfs.icmp, e = addProto("icmp"); e != nil {
			return nil, e
		}
		if nfs.icmpv6, e = addProto("icmpv6"); e != nil {
			return nil, e
		}
		if nfs.gre, e = addProto("gre"); e != nil {
			return nil, e
		}
		if nfs.esp, e = addProto("esp"); e != nil {
			return nil, e
		}
		if nfs.ipmux, e = addProto("ipmux"); e != nil {
			return nil, e
		}
		if nfs.rudp, e = addProto("rudp"); e != nil {
			return nil, e
		}
	}

	// /net/ether0
	nfs.ether0 = new(srv.File)
	if err := nfs.ether0.Add(nfs.net, "ether0", user, nil, p.DMDIR|0555, nil); err != nil {
		return nil, err
	}
	eaddr := new(EtherAddrFile)
	if err := eaddr.Add(nfs.ether0, "addr", user, nil, 0o444, eaddr); err != nil {
		return nil, err
	}
	eclone := new(EtherClone)
	eclone.nfs = nfs
	eclone.dir = nfs.ether0
	if err := eclone.Add(nfs.ether0, "clone", user, nil, 0o444, eclone); err != nil {
		return nil, err
	}
	estats := &ROTextFile{text: "ether stats: not implemented (netfs example)\n"}
	if err := estats.Add(nfs.ether0, "stats", user, nil, 0o444, estats); err != nil {
		return nil, err
	}
	eifstats := &ROTextFile{text: "ether ifstats: not implemented (netfs example)\n"}
	if err := eifstats.Add(nfs.ether0, "ifstats", user, nil, 0o444, eifstats); err != nil {
		return nil, err
	}

	// /net/bridge0
	nfs.bridge0 = new(srv.File)
	if err := nfs.bridge0.Add(nfs.net, "bridge0", user, nil, p.DMDIR|0555, nil); err != nil {
		return nil, err
	}
	blog := new(BridgeLog)
	if err := blog.Add(nfs.bridge0, "log", user, nil, 0o444, blog); err != nil {
		return nil, err
	}
	bctl := new(BridgeCtl)
	bctl.log = blog
	if err := bctl.Add(nfs.bridge0, "ctl", user, nil, 0o666, bctl); err != nil {
		return nil, err
	}
	bcache := &ROTextFile{text: ""} // empty cache
	if err := bcache.Add(nfs.bridge0, "cache", user, nil, 0o444, bcache); err != nil {
		return nil, err
	}
	bstats := &ROTextFile{text: "bridge stats: not implemented (netfs example)\n"}
	if err := bstats.Add(nfs.bridge0, "stats", user, nil, 0o444, bstats); err != nil {
		return nil, err
	}

	return nfs, nil
}

func main() {
	flag.Parse()

	nfs, err := buildNetFS()
	if err != nil {
		log.Fatalf("build netfs: %v", err)
	}

	nfs.srv = srv.NewFileSrv(nfs.root)
	nfs.srv.Dotu = true
	if *debug {
		nfs.srv.Debuglevel = 1
	}
	nfs.srv.Start(nfs.srv)

	if err := nfs.srv.StartNetListener("tcp", *addr); err != nil {
		log.Fatalf("listen %s: %v", *addr, err)
	}
}
