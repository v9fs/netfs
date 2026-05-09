package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lionkov/go9p/p"
	"github.com/lionkov/go9p/p/clnt"
	"github.com/lionkov/go9p/p/srv"
)

func mountNetFSClient(t *testing.T, addr string) (*clnt.Clnt, func()) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial 9p: %v", err)
	}
	c := clnt.NewClnt(conn, 8192, true)
	user := p.OsUsers.Uid2User(os.Geteuid())
	if _, err := c.Attach(nil, user, "/"); err != nil {
		_ = conn.Close()
		t.Fatalf("attach: %v", err)
	}
	return c, func() {
		c.Unmount()
		_ = conn.Close()
	}
}

func startNetFSServer(t *testing.T) (addr string, stop func()) {
	t.Helper()

	nfs, err := buildNetFS()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	nfs.srv = srv.NewFileSrv(nfs.root)
	nfs.srv.Dotu = true
	nfs.srv.Start(nfs.srv)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- nfs.srv.StartListener(ln) }()

	return ln.Addr().String(), func() {
		_ = ln.Close()
		if err := <-errCh; err != nil &&
			!errors.Is(err, net.ErrClosed) &&
			!errors.Is(err, os.ErrClosed) &&
			!strings.Contains(err.Error(), "use of closed network connection") {
			t.Fatalf("server: %v", err)
		}
	}
}

func startTCPEcho(t *testing.T) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()
	return ln.Addr().String(), func() {
		_ = ln.Close()
		<-done
	}
}

func TestNetFS_TCPConnectAndEcho(t *testing.T) {
	srvAddr, stop := startNetFSServer(t)
	defer stop()

	echoAddr, stopEcho := startTCPEcho(t)
	defer stopEcho()

	c, cleanup := mountNetFSClient(t, srvAddr)
	defer cleanup()

	clone, err := c.FOpen("/net/tcp/clone", p.OREAD)
	if err != nil {
		t.Fatalf("open clone: %v", err)
	}
	defer clone.Close()

	r := bufio.NewReader(clone)
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read clone: %v", err)
	}
	id := strings.TrimSpace(line)
	if id == "" {
		t.Fatalf("empty clone id")
	}

	ctl, err := c.FOpen("/net/tcp/"+id+"/ctl", p.OWRITE)
	if err != nil {
		t.Fatalf("open ctl: %v", err)
	}
	defer ctl.Close()

	if _, err := ctl.Write([]byte("connect " + echoAddr + "\n")); err != nil {
		t.Fatalf("ctl connect: %v", err)
	}

	data, err := c.FOpen("/net/tcp/"+id+"/data", p.ORDWR)
	if err != nil {
		t.Fatalf("open data: %v", err)
	}
	defer data.Close()

	want := []byte("hello-netfs\n")
	if _, err := data.Write(want); err != nil {
		t.Fatalf("write data: %v", err)
	}

	got := make([]byte, len(want))
	if _, err := io.ReadFull(data, got); err != nil {
		t.Fatalf("read data: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("got %q want %q", string(got), string(want))
	}
}

func TestNetFS_TCPCtlErrorFile(t *testing.T) {
	srvAddr, stop := startNetFSServer(t)
	defer stop()

	c, cleanup := mountNetFSClient(t, srvAddr)
	defer cleanup()

	clone, err := c.FOpen("/net/tcp/clone", p.OREAD)
	if err != nil {
		t.Fatalf("open clone: %v", err)
	}
	line, err := bufio.NewReader(clone).ReadString('\n')
	_ = clone.Close()
	if err != nil {
		t.Fatalf("read clone: %v", err)
	}
	id := strings.TrimSpace(line)

	ctl, err := c.FOpen("/net/tcp/"+id+"/ctl", p.OWRITE)
	if err != nil {
		t.Fatalf("open ctl: %v", err)
	}
	_, err = ctl.Write([]byte("connect not-a-host\n"))
	_ = ctl.Close()
	if err == nil {
		t.Fatalf("expected connect to fail")
	}

	ef, err := c.FOpen("/net/tcp/"+id+"/error", p.OREAD)
	if err != nil {
		t.Fatalf("open error: %v", err)
	}
	b, err := io.ReadAll(ef)
	_ = ef.Close()
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if strings.TrimSpace(string(b)) == "ok" {
		t.Fatalf("expected error detail, got %q", string(b))
	}
}

func TestNetFS_NDB_Limit(t *testing.T) {
	srvAddr, stop := startNetFSServer(t)
	defer stop()

	c, cleanup := mountNetFSClient(t, srvAddr)
	defer cleanup()

	f, err := c.FOpen("/net/ndb", p.OWRITE)
	if err != nil {
		t.Fatalf("open /net/ndb: %v", err)
	}
	defer f.Close()

	tooBig := bytes.Repeat([]byte("x"), 1025)
	if _, err := f.Write(tooBig); err == nil {
		t.Fatalf("expected write error for >1024 bytes")
	}
}

func TestNetFS_IPSelftab_Readable(t *testing.T) {
	srvAddr, stop := startNetFSServer(t)
	defer stop()

	c, cleanup := mountNetFSClient(t, srvAddr)
	defer cleanup()

	f, err := c.FOpen("/net/ipselftab", p.OREAD)
	if err != nil {
		t.Fatalf("open ipselftab: %v", err)
	}
	defer f.Close()

	if _, err = io.ReadAll(f); err != nil {
		t.Fatalf("read ipselftab: %v", err)
	}
}

func TestNetFS_IPIFC_StatusPresent(t *testing.T) {
	srvAddr, stop := startNetFSServer(t)
	defer stop()

	c, cleanup := mountNetFSClient(t, srvAddr)
	defer cleanup()

	d, err := c.FOpen("/net/ipifc", p.OREAD)
	if err != nil {
		t.Fatalf("open ipifc: %v", err)
	}
	defer d.Close()

	ents, err := d.Readdir(0)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("readdir ipifc: %v", err)
	}
	foundStats := false
	foundNumbered := ""
	for _, e := range ents {
		if e.Name == "stats" {
			foundStats = true
		}
		if len(foundNumbered) == 0 && regexp.MustCompile(`^\d+$`).MatchString(e.Name) {
			foundNumbered = e.Name
		}
	}
	if !foundStats {
		t.Fatalf("expected /net/ipifc/stats to exist")
	}
	if foundNumbered == "" {
		t.Fatalf("expected at least one numbered /net/ipifc/<n> directory")
	}

	st, err := c.FOpen("/net/ipifc/"+foundNumbered+"/status", p.OREAD)
	if err != nil {
		t.Fatalf("open ipifc status: %v", err)
	}
	defer st.Close()
	_, _ = io.ReadAll(st)
}

func TestNetFS_Ether0_AddrAndType(t *testing.T) {
	srvAddr, stop := startNetFSServer(t)
	defer stop()

	c, cleanup := mountNetFSClient(t, srvAddr)
	defer cleanup()

	af, err := c.FOpen("/net/ether0/addr", p.OREAD)
	if err != nil {
		t.Fatalf("open ether0 addr: %v", err)
	}
	b, _ := io.ReadAll(af)
	_ = af.Close()
	s := strings.TrimSpace(string(b))
	if len(s) != 12 {
		t.Fatalf("expected 12 hex chars mac, got %q", s)
	}
	if !regexp.MustCompile(`^[0-9a-fA-F]{12}$`).MatchString(s) {
		t.Fatalf("expected hex mac, got %q", s)
	}

	clone, err := c.FOpen("/net/ether0/clone", p.OREAD)
	if err != nil {
		t.Fatalf("open ether clone: %v", err)
	}
	r := bufio.NewReader(clone)
	idLine, err := r.ReadString('\n')
	_ = clone.Close()
	if err != nil {
		t.Fatalf("read ether clone: %v", err)
	}
	id := strings.TrimSpace(idLine)
	if id == "" {
		t.Fatalf("empty ether id")
	}

	ctl, err := c.FOpen("/net/ether0/"+id+"/ctl", p.OWRITE)
	if err != nil {
		t.Fatalf("open ether ctl: %v", err)
	}
	if _, err := ctl.Write([]byte("connect 2048\n")); err != nil {
		_ = ctl.Close()
		t.Fatalf("write connect: %v", err)
	}
	_ = ctl.Close()

	// Malformed connect should fail and record details in /error.
	ctl, err = c.FOpen("/net/ether0/"+id+"/ctl", p.OWRITE)
	if err != nil {
		t.Fatalf("open ether ctl (2): %v", err)
	}
	_, err = ctl.Write([]byte("connect\n"))
	_ = ctl.Close()
	if err == nil {
		t.Fatalf("expected malformed connect to fail")
	}
	ef, err := c.FOpen("/net/ether0/"+id+"/error", p.OREAD)
	if err != nil {
		t.Fatalf("open ether error: %v", err)
	}
	eb, err := io.ReadAll(ef)
	_ = ef.Close()
	if err != nil {
		t.Fatalf("read ether error: %v", err)
	}
	if !strings.Contains(string(eb), "expected type") {
		t.Fatalf("error=%q", string(eb))
	}

	tf, err := c.FOpen("/net/ether0/"+id+"/type", p.OREAD)
	if err != nil {
		t.Fatalf("open ether type: %v", err)
	}
	tb, _ := io.ReadAll(tf)
	_ = tf.Close()
	if strings.TrimSpace(string(tb)) != "2048" {
		t.Fatalf("expected type=2048, got %q", strings.TrimSpace(string(tb)))
	}
}

func TestNetFS_Bridge0_CtlLogs(t *testing.T) {
	srvAddr, stop := startNetFSServer(t)
	defer stop()

	c, cleanup := mountNetFSClient(t, srvAddr)
	defer cleanup()

	ctl, err := c.FOpen("/net/bridge0/ctl", p.OWRITE)
	if err != nil {
		t.Fatalf("open bridge ctl: %v", err)
	}
	line := "bind ether test0 0 /net/ether0\n"
	if _, err := ctl.Write([]byte(line)); err != nil {
		_ = ctl.Close()
		t.Fatalf("write bridge ctl: %v", err)
	}
	_ = ctl.Close()

	logf, err := c.FOpen("/net/bridge0/log", p.OREAD)
	if err != nil {
		t.Fatalf("open bridge log: %v", err)
	}
	b, _ := io.ReadAll(logf)
	_ = logf.Close()
	if !strings.Contains(string(b), strings.TrimSpace(line)) {
		t.Fatalf("expected bridge log to contain ctl line; got %q", string(b))
	}
}

func TestNetFS_ProtocolDirs_Present(t *testing.T) {
	srvAddr, stop := startNetFSServer(t)
	defer stop()

	c, cleanup := mountNetFSClient(t, srvAddr)
	defer cleanup()

	for _, proto := range []string{"udp", "icmp", "icmpv6", "gre", "esp", "ipmux", "rudp"} {
		stats, err := c.FOpen("/net/"+proto+"/stats", p.OREAD)
		if err != nil {
			t.Fatalf("open %s stats: %v", proto, err)
		}
		_, _ = io.ReadAll(stats)
		_ = stats.Close()

		clone, err := c.FOpen("/net/"+proto+"/clone", p.OREAD)
		if err != nil {
			t.Fatalf("open %s clone: %v", proto, err)
		}
		buf := make([]byte, 1)
		_, rerr := clone.Read(buf)
		_ = clone.Close()
		if rerr == nil {
			t.Fatalf("expected %s clone read to error (stub)", proto)
		}
	}
}
