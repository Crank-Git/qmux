# 0005 — Define the pkg/transport API for the server and the client

Date: 2026-11-20
Status: accepted

## Context

ADR 0001 confines `github.com/miekg/dns` v1.1.73 to `pkg/transport`. The spec gives
`pkg/transport` four duties: it builds queries, packs opaque RDATA, applies EDNS(0) and
the UDP size limit, and matches answers. The spec gives no Go API for these duties.

An earlier attempt put the wire code in `pkg/server`. It set `RFC3597.Rdata` to the raw
payload, not to hex. It discarded every `WriteMsg` error. Its transport functions took
`*dns.Msg`, so miekg types leaked to the callers.

`dns.Server` does not suit QMux. It reads a UDP query into a buffer of `UDPSize` bytes
and does not give the received size to the handler. `Msg.Unpack` ignores trailing bytes.
So FR-server-16 and FR-server-17 cannot measure the query exactly. When the accept
function rejects a message or a message does not parse, `dns.Server` writes FORMERR or
NOTIMP itself and ignores the write error.

## Decision

`pkg/transport` exports QMux-native types only. No exported identifier names a miekg
type. `pkg/transport` runs its own UDP and TCP read loops and uses miekg only for
`Msg.Pack`, `Msg.Unpack`, `Msg.Truncate`, `SetEdns0` and `RFC3597`. `pkg/server` keeps
the validation policy and the RCODE choice. `pkg/client` keeps retries, TCP fallback and
`DNSError`.

```go
package transport // imports github.com/miekg/dns and pkg/protocol only

type RCode uint16 // RCodeNoError, RCodeFormErr, RCodeServFail, RCodeNotImp, RCodeRefused, RCodeBadVers
func (r RCode) String() string // "NOERROR", "BADVERS", ...
const (UDP = "udp"; TCP = "tcp")
var (ErrMalformed, ErrMismatch, ErrAlreadyWritten error)

// The server side.
type Query struct {
    ID, Opcode        uint16 // Opcode stays as received; the server answers NOTIMP
    QR                bool
    QuestionCount     int
    Name              string               // QNAME as received, from question 1
    Key               protocol.ProtocolKey // QCLASS and QTYPE of question 1
    EDNS              bool
    EDNSVersion       uint8
    UDPSize           uint16 // advertised; 0 without EDNS(0)
    WireSize          int    // exact bytes received
    Source            netip.AddrPort
    Transport         string // UDP or TCP
    ReceivedAt        time.Time
    Err               error // wraps ErrMalformed: the header parsed, the body did not
}
type Answer struct { RCode RCode; TTL uint32; Payload []byte } // RR only for NOERROR
type Sent struct { RCode RCode; Truncated bool; WireSize int }
type Writer interface { Write(a Answer) (Sent, error) }
type Handler interface { ServeQuery(ctx context.Context, q *Query, w Writer) }
type HandlerFunc func(ctx context.Context, q *Query, w Writer)
type ServeConfig struct {
    UDPSize                   uint16 // server UDP size limit with EDNS(0), and the OPT RR value
    ReadTimeout, WriteTimeout time.Duration
    ShutdownGrace             time.Duration
    Logger                    *slog.Logger
}
func Listen(addr string) (net.PacketConn, net.Listener, error) // TCP on the UDP port
func Serve(ctx context.Context, pc net.PacketConn, l net.Listener, h Handler, cfg ServeConfig) error
func ServeMessage(ctx context.Context, wire []byte, src netip.AddrPort, transport string,
    h Handler, cfg ServeConfig) ([]byte, error) // one message, no socket

// The client side.
type Packet struct { ID uint16; Name string; Key protocol.ProtocolKey; Wire []byte }
func NewQuery(name string, key protocol.ProtocolKey, udpSize uint16, recursion bool) (Packet, error)
type Received struct { RCode RCode; Truncated bool; TTL uint32; Payload []byte; WireSize int }
func ExchangeUDP(ctx context.Context, c net.Conn, p Packet) (Received, error)
func ExchangeTCP(ctx context.Context, c net.Conn, p Packet) (Received, error)
```

### The rules for `Write`

1. The response copies the ID and the question. It sets RA to 0.
2. For `RCodeNoError`, the response sets AA and holds one RR. The RR has the QNAME as
   received, the class and type of the question, the TTL and the payload.
3. The RR is a `*dns.RFC3597`. `Rdata` is `hex.EncodeToString(Payload)`.
4. If the query has EDNS(0), the response holds an OPT RR with `cfg.UDPSize`. BADVERS
   goes in the extended RCODE of that OPT RR.
5. If the query has no EDNS(0), the UDP size limit is 512. Otherwise the limit is the
   smaller of `UDPSize` and `cfg.UDPSize`.
6. If a UDP response exceeds the limit, `Msg.Truncate` sets TC, removes the RR and keeps
   the OPT RR.
7. If a TCP response exceeds 65535 bytes, `Write` sends SERVFAIL. `Sent.RCode` records
   SERVFAIL.
8. `Write` returns every pack and write error, wrapped. A second `Write` returns
   `ErrAlreadyWritten`.

`Serve` reads into a 65535-byte buffer. It drops a packet that is shorter than the
12-byte header, and logs it at debug level. After `ctx` ends, `Serve` closes `l` and
stops the reads on `pc`. It waits `ShutdownGrace` for in-flight calls, then cancels their
contexts. It closes `pc` after the calls return, so that a UDP handler can still send its
answer during `ShutdownGrace`. Handler
contexts do not come from the `Serve` context.

### The rules for `Exchange`

1. `ExchangeUDP` ignores a response with a different ID until `ctx` expires.
   `ExchangeTCP` also ignores a response with a different ID, because an earlier
   exchange on the connection can leave a late response.
2. A response that does not parse returns `ErrMalformed`.
3. TC over TCP returns `ErrMalformed`.
4. If the question count is not 1, or the name, class or type differs, the function
   returns `ErrMismatch`. Names compare without regard to case. One exception applies:
   if the RCODE is not NOERROR and the response holds no question, the function returns
   `Received` with that RCODE and no error. An absent question does not differ from the
   query, so the RCODE applies (FR-client-17, FR-client-21). For example, BIND can
   answer NOTIMP without the question.
5. For NOERROR, the response must hold exactly one answer RR with the question name,
   class and type. Otherwise the function returns `ErrMismatch`. One exception applies:
   if a UDP response has the TC bit, the function returns `Received` with `Truncated`
   true and no error. Write rule 6 removes the answer RR from a truncated response, and
   the client then retries over TCP.
6. `Payload` comes from the hex `Rdata`. For a type that miekg knows, for example TXT,
   the payload is the packed RDATA of the RR.

### Tests without miekg

- Unit tests in `pkg/server` build a `transport.Query` literal and use a fake `Writer`.
- `FuzzServeMessage` calls `transport.ServeMessage` with the server handler.
- Package `pkg/transport/transporttest` imports miekg for test code only. It exports
  `Message`, `RR`, `Pack`, `Unpack`, `Exchange` and `StartServer`.
- `StartServer(t, Responder)` listens on `127.0.0.1:0` over UDP and TCP. The responder
  can drop a packet, send a wrong ID, or send raw bytes.
- `Message.PadTo` makes a parseable oversized query.

### The boundary rule in `.golangci.yml`

```yaml
linters:
  enable: [depguard]          # add to the existing list
  settings:
    depguard:
      rules:
        miekg-only-in-transport:
          files: ["$all", "!**/pkg/transport/**"]
          deny:
            - pkg: github.com/miekg/dns
              desc: Only pkg/transport imports miekg/dns. See ADR 0001 and ADR 0005.
```

### FR ownership

| Owner | FRs |
|---|---|
| `pkg/transport` | FR-server-6, 7, 11, 12, 31, 37, 38, 39, 40, 41; FR-client-5, 7, 16, 17, 18, 19, 20, 22, 26, 31 |
| `pkg/server` | Every other FR-server. FR-server-6 to 12 remain the public API and delegate. |
| `pkg/client` | Every other FR-client. The client maps `ErrMismatch` and `ErrMalformed` to `DNSError` kinds. |

No FR text and no acceptance criterion changes. Each FR keeps its ID in its feature file.

### Rejected alternatives

- **`pkg/server` imports miekg directly.** This breaks ADR 0001: a port to the codeberg
  module would change several packages. The earlier attempt shows the cost: the wire bugs
  were in the policy code, and no test without sockets found them.
- **`pkg/transport` re-exports miekg types** (`type Msg = dns.Msg`, functions on
  `*dns.Msg`). Each caller must still know the hex `Rdata`, the `Truncate` rules and the
  `SetEdns0` rules. depguard cannot see a use of the alias.
- **`pkg/transport` wraps `dns.Server` with a custom `MsgAcceptFunc`.** The query wire
  size is lost, and `dns.Server` writes replies whose errors QMux cannot see. See Context.

## Consequences

- A port to `codeberg.org/miekg/dns` changes `pkg/transport` and `transporttest` only.
- `pkg/server` and `pkg/client` tests run with no miekg import and mostly with no socket.
- `pkg/transport` must implement TCP framing and the shutdown wait. It needs its own
  race tests.
- `pkg/server` must answer NOTIFY and IQUERY with NOTIMP, because no accept function
  rejects them first.
- The sentence about `MsgAcceptFunc` in `02-authoritative-server.md` and the `dns.Server`
  and `dns.Client` facts in the feature files no longer describe the build. The change
  that adds this ADR repairs those sentences.
