# go9p-netfs

A 9P synthetic filesystem that models a subset of Plan 9's `/net` device
interfaces (`ip(3)`, `ether(3)`, `bridge(3)`) on top of Linux userland
networking, served with `[go9p](https://github.com/lionkov/go9p)`.

This was originally an example under `p/srv/examples/netfs/` in
`go9p` and is now maintained as a standalone project so it can grow
independently of the core 9P library.

See `[DESIGN.md](./DESIGN.md)` for the full design notes (intent,
filesystem shape, semantics, and limitations).

## Filesystem shape (current)

```text
/net
  tcp/           working subset: clone-allocated conversations (ctl/data/local/remote/status)
  ndb            small read/write config blob
  log            simple log buffer + controls
  ipifc/         read-only projection of host interfaces
  ipselftab      read-only local address listing
  arp            stub
  iproute        stub
  ether0/        minimal ether(3) surface (mostly stub)
  bridge0/       minimal bridge(3) surface (mostly stub)
  udp/ icmp/ ... protocol dirs (placeholders)
```

The TCP conversation pattern (`/net/tcp/clone` + per-id `ctl`/`data`) is
fully implemented and tested end-to-end against the Go 9P client.

## Run

```bash
go run ./cmd/go9p-netfs -addr 127.0.0.1:5640
```

Flags:

- `-addr <host:port>` — listen address (default `:5640`)
- `-d` — print 9P debug messages

From another terminal you can drive it with any `go9p` client, for
example the session helper from your `go9p` checkout:

```bash
go run /Users/erivan01/src/v9fs/go9p/cmd/go9p-session -net tcp -addr 127.0.0.1:5640
```

Inside the session:

```text
read /net/tcp/clone          # allocates /net/tcp/<id>/
write /net/tcp/<id>/ctl connect example.com!80
write /net/tcp/<id>/data GET / HTTP/1.0\r\nHost: example.com\r\n\r\n
read /net/tcp/<id>/data
write /net/tcp/<id>/ctl close
```

## Mount with the Linux kernel 9p client

```bash
sudo mount -t 9p -o trans=tcp,port=5640,version=9p2000.u,uname=$USER 127.0.0.1 /mnt/netfs
ls /mnt/netfs/net
```

`netfs` is intentionally light on kernel-write paths — the e2e smoke
tests focus on safe reads and clone allocation rather than relying on
kernel write compatibility for every synthetic node.

## Tests

```bash
go test ./...
```

The userspace tests exercise the TCP conversation flow (clone, connect,
data, close, status) end-to-end against an in-process `go9p` client.

## Status

This module currently builds against the `rework` branch of
`github.com/ericvh/go9p` (pinned via a `replace` directive in
`go.mod`) until the changes there are merged upstream into
`github.com/lionkov/go9p`.