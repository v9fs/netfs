# `netfs` design notes

`netfs` is a synthetic 9P filesystem built with `go9p/p/srv` that models a subset of Plan 9’s `/net`
interfaces (documented in `ip(3)`, `ether(3)`, `bridge(3)`) using Linux userland networking primitives.

This file documents the **intent**, the **filesystem shape**, and the **semantics** behind the example.

## Goals

- Demonstrate how to model a complex “device-like” interface as a hierarchical `srv.File` tree.
- Implement a working instance of the Plan 9 **clone + per-instance directory** pattern.
- Provide a small, testable subset of `/net` that is useful as a template for further expansion.

## Non-goals

- Full fidelity Plan 9 networking stack emulation.
- Kernel-level features such as raw packet injection, route/ARP management, or netlink integration.
- Perfect parity with Plan 9 semantics across all files (many entries are intentionally stubbed).

## Top-level hierarchy

At a high level:

```text
/net
  tcp/           (working subset: conversations)
  ndb            (small read/write config blob)
  log            (simple log buffer + controls)
  ipifc/         (read-only, derived from net.Interfaces)
  ipselftab      (read-only local address listing)
  arp            (stub)
  iproute        (stub)
  ether0/        (minimal ether(3) surface; mostly stub)
  bridge0/       (minimal bridge(3) surface; mostly stub)
  udp/ icmp/ ... (protocol dirs; placeholders)
```

The “shape first, semantics later” approach is deliberate: in Plan 9, the namespace itself is part of the
user interface. This example makes the hierarchy concrete, even when some parts are stubs.

## The core implemented pattern: TCP conversations

The main implemented subsystem is the TCP conversation pattern:

```text
/net/tcp
  clone              (read: allocates a new conversation id)
  <id>/
    ctl              (write: connect/close)
    data             (read/write: stream bytes over TCP)
    local            (read: local addr)
    remote           (read: remote addr)
    status           (read: state summary)
```

### Allocation (`clone`)

Reading `/net/tcp/clone` allocates a new conversation directory `/net/tcp/<id>/`.

This is the canonical Plan 9 “factory file” pattern:

- the file is not stored data
- the act of reading it *creates a new live object*
- the returned value (the id) is a stable handle for subsequent operations

### Control (`ctl`)

`/net/tcp/<id>/ctl` is a low-rate textual control surface.

Supported commands (subset):

- `connect <addr>`: connect to a remote endpoint (accepts `host:port`, `host port`, or `host!port`)
- `close`: close the underlying socket (conversation remains, but becomes disconnected)

### Data (`data`)

`/net/tcp/<id>/data` is a stream interface to the connected TCP socket.

Important semantic point: `data` behaves like a stream, so **offset is ignored** (similar to how many
Plan 9 stream files behave). Reads and writes are forwarded to the socket.

### Diagnostics (`status`, `local`, `remote`)

These files make the conversation state inspectable without requiring a separate API:

- `status`: human-readable summary
- `local` / `remote`: address strings

## Stubbed / minimal surfaces

The rest of `/net` exists in a “minimal but visible” form:

- **`/net/ndb`**: a small read/write blob (bounded) to illustrate config-like files.
- **`/net/log`**: a simple log buffer with basic controls (`set/clear/only`).
- **`/net/ipifc`**: a read-only projection of host interfaces (`net.Interfaces()`) to show how an
  underlying OS object model can be reflected into a tree of numbered directories and status files.
- **`/net/ether0`**, **`/net/bridge0`**, and protocol dirs (`udp`, `icmp`, …): placeholders that
  establish the expected layout but do not yet implement packet/forwarding stacks.

## Testing & caveats

- **Userspace tests** (`go test ./p/srv/examples/netfs`): validate the TCP conversation flow end-to-end
  using the Go 9P client.
- **Kernel-client e2e**: `netfs` can be mounted by the Linux kernel 9p client; the e2e smoke test focuses
  on safe reads and clone allocation rather than relying on kernel write compatibility for all synthetic nodes.

Because `/net` is inherently “live”, several operations are intentionally implemented as protocol steps rather
than passive file reads/writes. That is the core lesson of the example.

