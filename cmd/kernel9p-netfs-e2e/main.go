package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func must(err error, msg string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %s: %v\n", msg, err)
		os.Exit(1)
	}
}

func mustEq[T comparable](got, want T, msg string) {
	if got != want {
		fmt.Fprintf(os.Stderr, "FAIL: %s: got=%v want=%v\n", msg, got, want)
		os.Exit(1)
	}
}

func readTrimLine(path string) string {
	f, err := os.Open(path)
	must(err, "open "+path)
	defer f.Close()

	s, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && err != io.EOF {
		must(err, "readline "+path)
	}
	return strings.TrimSpace(s)
}

func mustExistDir(path string, msg string) {
	st, err := os.Stat(path)
	must(err, msg+" (stat)")
	if !st.IsDir() {
		must(fmt.Errorf("not a directory"), msg)
	}
}

func main() {
	mount := os.Getenv("KERNEL9P_MOUNT")
	if mount == "" {
		mount = "/tmp/netfs-kernel-e2e"
	}
	tcpAddr := os.Getenv("KERNEL9P_TCP_ADDR")
	if tcpAddr == "" {
		tcpAddr = "10.0.2.2"
	}
	tcpPort := os.Getenv("KERNEL9P_TCP_PORT")
	if tcpPort == "" {
		tcpPort = "564"
	}
	echoPort := os.Getenv("NETFS_ECHO_PORT")
	if echoPort == "" {
		echoPort = "7777"
	}

	fmt.Printf("INFO: kernel9p-netfs-e2e mount=%s tcp=%s:%s echo=127.0.0.1:%s\n", mount, tcpAddr, tcpPort, echoPort)

	// Mount the netfs server root over the kernel 9p TCP client.
	must(os.MkdirAll(mount, 0o777), "mkdir mountpoint")
	mustExistDir(mount, "mountpoint is dir")

	must(platformMount9P(tcpAddr, tcpPort, mount), "mount netfs via tcp")

	// Basic shape checks.
	mustExistDir(filepath.Join(mount, "net"), "netfs root has /net")
	mustExistDir(filepath.Join(mount, "net", "tcp"), "netfs has /net/tcp")

	clonePath := filepath.Join(mount, "net", "tcp", "clone")
	id := readTrimLine(clonePath)
	if id == "" {
		must(fmt.Errorf("empty clone id"), "clone returns id")
	}
	convDir := filepath.Join(mount, "net", "tcp", id)
	mustExistDir(convDir, "clone creates /net/tcp/<id>")

	ctlPath := filepath.Join(convDir, "ctl")
	dataPath := filepath.Join(convDir, "data")
	_, err := os.Stat(ctlPath)
	must(err, "conv has ctl")
	_, err = os.Stat(dataPath)
	must(err, "conv has data")

	// End-to-end TCP connect + echo (kernel-mounted IO -> server dials localhost echo).
	ctl, err := os.OpenFile(ctlPath, os.O_WRONLY, 0)
	must(err, "open ctl write")
	_, err = io.WriteString(ctl, "connect 127.0.0.1:"+echoPort+"\n")
	must(err, "ctl connect write")
	must(ctl.Close(), "close ctl")

	data, err := os.OpenFile(dataPath, os.O_RDWR, 0)
	must(err, "open data rdwr")
	defer data.Close()

	// Give the server a moment to establish the outbound TCP connection.
	time.Sleep(50 * time.Millisecond)

	want := []byte("hello-from-kernel-netfs\n")
	_, err = data.Write(want)
	must(err, "write data")

	got := make([]byte, len(want))
	_, err = io.ReadFull(data, got)
	must(err, "read echo")
	mustEq(string(got), string(want), "echo roundtrip")

	fmt.Println("PASS: kernel9p netfs e2e")
}
