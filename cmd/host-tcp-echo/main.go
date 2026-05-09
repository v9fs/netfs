package main

import (
	"io"
	"log"
	"net"
	"os"
	"time"
)

func main() {
	addr := os.Getenv("ECHO_ADDR")
	if addr == "" {
		addr = "127.0.0.1:7777"
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen %s: %v", addr, err)
	}
	log.Printf("listening on %s", ln.Addr().String())

	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatalf("accept: %v", err)
		}
		go func(conn net.Conn) {
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
			_, _ = io.Copy(conn, conn)
		}(c)
	}
}

