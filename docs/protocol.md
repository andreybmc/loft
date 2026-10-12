# loft: an HTTPS overlay

**English** · [Русский](protocol_ru.md)

**loft** is Layered Overlay For TLS. The wire protocol is **LFTP** (LOFT Tunneling Protocol). Both names refer to this specification.

**Protocol 1.0.** A third-party implementation that follows this document can speak to a loft node. The Go tree in this repository is one implementation. Licensed [Apache-2.0](../LICENSE).

Software version (`VERSION`, currently 0.1.0) is independent of protocol version. Handshake carries both. Keywords **MUST**, **SHOULD**, **MAY** follow RFC 2119. Unknown trailing fields in Hello and Status MUST be ignored.

---

## Abstract

A private overlay can be blocked cheaply if it occupies a unique place on the wire. loft occupies the same place as ordinary HTTPS: TLS 1.3 on port 443, a browser-shaped ClientHello, and application data that a CDN may terminate. Inner streams are multiplexed and sealed with ChaCha20-Poly1305. A node MAY present the identity of a real HTTPS origin; probes without a member key are spliced to that origin. Identity of the overlay (keys, leases, the list of live fronts) lives on a controller. Relays are disposable TLS edges. Clients fetch a signed roster of fronts and named address lists; they map those lists to local routing. Members of one network MAY be isolated at a hub, meshed through the node, or allowed a direct TCP attempt.

---

## 1. Introduction

Encrypted tunnels fail in two ways. A distinctive handshake is classified and dropped. A tunnel that uses a unique destination IP is classified by address and dropped. Both failures are cheap for a path middlebox.

The remaining expensive cases are collateral: blocking a name that a large population uses in the clear, or blocking an IP that also serves an unrelated public site. loft is built around those cases. The inner protocol is one. The path that carries it (GET, WebSocket, HTTP/2 CONNECT, QUIC, HTTP/3 CONNECT) is chosen per deployment and MAY change without a protocol bump. The TLS name a client sends is chosen by the node. A probe that lacks the member key completes a handshake with a configured origin.

A second cost is operational. An IP that has been classified is cheap to replace if clients already know another front and keep the same overlay address. The controller signs a roster of fronts. Relays enrol, send heartbeats, and leave the roster when silent. The member key never appears in that document.

This document specifies the wire. Building and operating the reference node is described in [guide.md](guide.md). Package layout of the reference tree is described in [architecture.md](architecture.md).

---

## 2. Construction

```
application  TCP / UDP     (SOCKS and TUN are local)
    mux      Open Data Close Reset Ping Pong Pad Dgram Leave
      AEAD   ChaCha20-Poly1305
        front  get | ws | h2 | quic | h3
          TLS 1.3
            TCP/443 or UDP/443
```

The TLS ClientHello SHOULD match a current browser (cipher list, extensions, JA3/JA4). CDN vendor names are deployment profiles. They MUST NOT appear as protocol capabilities.

A session has three layers:

1. **TLS.** Public. A CDN, a load balancer, or an origin MAY terminate it.
2. **Auth and AEAD.** Inner. Possession of the member key proves membership. ChaCha20-Poly1305 hides mux from the terminator of TLS.
3. **Mux.** Streams and datagrams between the client and the node (and, through the node, the Internet or other members).

Reserved mux targets on a relay trunk: `hub:1` (HTTP/1.1), `hub:2` (HTTP/2), `ctl:1` (fleet control). Overlay announce: `loft.peer:<port>`.

String fields on Hello, Status, and most fleet records are `u8 length || bytes`, at most 64 bytes. URLs and list values are `u16be length || bytes`, at most 255 bytes unless a section states otherwise. Integers are big-endian.

---

## 3. Transports

A node advertises enabled fronts in Status `caps`. A client advertises the fronts it will use in Hello `caps`. After the first Status the client MUST use only the intersection, in its configured order, and MUST move to the next remaining front when the current one fails.

Default try order, when both sides enable every front: `get`, `ws`, `h2`, `quic`, `h3`.

### 3.1 Decoy splice

Let `token` be the member key, `R` the 32-byte `client_random`, `S` the lowercase SNI, `X` the X25519 public share from ClientHello. A loft ClientHello has `session_id` of 32 bytes:

```
HKDF-SHA256(ikm=token, salt=R, info="loft-sid-v2" || 0x00 || S || X, L=32)
```

A ClientHello whose `session_id` equals this value is loft. Any other TCP ClientHello is spliced to an origin the node configured. The first origin is the node’s identity. A matching SNI among the node’s origins selects that origin; otherwise the primary is used. Origin hostnames are configuration. They MUST NOT appear as protocol constants.

Without `token` the `session_id` is a 32-byte string with the same shape as Chrome’s compatibility `session_id`. The client MUST send the node’s primary decoy hostname as SNI on a direct path. It learns that hostname from Status, from the roster, or from the certificate a probe would see. After Status, a mismatch MUST reconnect with the advertised name. A CDN front keeps the CDN hostname.

Possession of `token` allows detection of every ClientHello that uses it. The binding to `R`, `S`, and `X` prevents a recipe of the form HMAC(`token`, `random`) from matching.

### 3.2 Decoy mask

When mask is enabled, a loft ClientHello completes TLS presenting the origin’s certificate. CertificateVerify is Ed25519 under the node’s mask key. Status `mask_pub` and roster `mask_pub` carry that 32-byte public key. Empty means mask is off. Probes continue to splice. Mask is a non-standard handshake; an implementation that only splices remains compatible with splice-only nodes.

### 3.3 WebSocket (`ws`)

TLS, then HTTP upgrade. Path is node-local (default `/api/v1/sync`). Client message 1 is the auth blob in the clear. Node message 1 is AEAD(Status). Further messages are AEAD(mux batch). Empty batches MUST NOT be sent. Closing the socket ends the session. The client SHOULD send CmdLeave in a mux batch before that close.

### 3.4 GET (`get`)

For edges that forward GET only. HTTP/2. Paths look like assets: `{base}/{session}/{seq}.js`.

First GET: header `X-Auth: 1`, Cookie `session=<base64url(auth blob)>`. Response 200, `X-Session`, `Cache-Control: private, no-store`, body AEAD(Status) with AAD = session id.

Later GET: `X-Request-Id: base64url(AEAD(mux batch))`, AAD = session id, body AEAD(mux batch). A node MAY also accept that uplink in Cookie `session=` (older clients); `X-Auth: 1` is absent. Failed auth MUST be 404 with HTML. Uplink MUST fit one header (default 12 KiB decoded mux plaintext, before AEAD and base64url). HTTP 413: halve the budget, floor 1024, and split. HTTP 405: this front is not GET. Inflight 2–8 (protocol default 4; a client picks a value in this range). Idle uplink empty: wait at least `keepalive_ms / 2` (and not less than 1 s). A GET that already carries mux Data, Open, Dgram, or Leave is not idle. The node SHOULD hold a GET up to 250 ms for downlink and MUST NOT hold more than 1 s. When the uplink already contains CmdData, the node MAY return as soon as downlink Data is queued.

Dropping HTTP/2 without Leave does not end the session. Before the client process exits it MUST send one last GET whose uplink batch contains CmdLeave. The node then closes the mux session, forgets the GET session id, Detach, and Release. `lease_hold` still keeps the overlay IP for `ok_resumed`.

### 3.5 HTTP/2 CONNECT (`h2`)

ALPN `h2`. CONNECT to a node-local path (default `/api/v1/tunnel`). 200, then messages `u32be length || bytes`: auth, AEAD(Status), AEAD(mux batch).

### 3.6 QUIC mux (`quic`)

First client-initiated bidi stream, same length-prefixed messages as `h2`. ALPN MUST be `h3`. If HTTP/3 CONNECT is also enabled on that UDP port, CONNECT takes the ALPN and this mux is not served.

### 3.7 HTTP/3 CONNECT (`h3`)

ALPN `h3`. CONNECT to the same path shape as `h2`.

### 3.8 Reassembly flags

Status MAY set `dpi_flags`: bit 0 fat ClientHello, bit 1 ClientHello split across two TLS records (the node concatenates). `dpi_pad` is extra padding bytes, 0–2048. These increase middlebox work. They do not hide SNI from a reassembling inspector.

### 3.9 CONNECT splice (`connect`)

GET mux polls a stream protocol over request/response. A node MAY set Status cap `connect`. After the usual GET/WS handshake (lease, overlay IP), a client MAY Open TCP with HTTP/2 `CONNECT` `:authority` `host:port` instead of mux `CmdOpen`. The node dials that target and splices bytes. Overlay member IPs, UDP, and ICMP stay on mux. A node without `connect` keeps mux for every TCP.

---

## 4. Authentication and confidentiality

A member key is `lt_` followed by 48 hex characters (24 bytes). The node stores SHA-256(key). A deployment MAY use one token for every member; that is configuration.

### 4.1 Clockless proof

```
0x02 || nonce[16] || HMAC-SHA256(key, nonce)[0:16] || Hello
```

Replay is prevented by a nonce cache on the node. Default TTL is 10 minutes on the node’s clock. Replicas share resume tickets, not this cache. The client clock is unused.

### 4.2 Resume ticket

```
0x03 || exp_unix_u64be || nonce[16] || u8 instance_len || instance || HMAC-SHA256(key, exp||nonce||instance)[0:16]
```

The ticket is HMAC under the member key. Expiry uses the issuer’s clock. The client echoes the bytes. Any replica that holds the member key can open it.

### 4.3 Session key

```
HKDF-SHA256(ikm=member_key, salt=nonce, info="loft-v1", L=32)
```

Seal: `nonce[12] || ChaCha20-Poly1305(key, nonce, plaintext, aad)` (RFC 8439). Nonce is random per seal. GET uses AAD = session id; other fronts use empty AAD unless a section states otherwise.

AEAD covers mux. TLS covers the path to the terminator. A CDN that terminates TLS sees ciphertext of mux.

---

## 5. Hello and Status

Hello is appended to the auth blob. Status is the first AEAD plaintext from the node. Neither is a mux command.

### 5.1 Hello

```
u8    version          (= 1)
u8    member_id_len || member_id
u8    instance_id_len || instance_id
u16be proto_min
u16be proto_max
u64be caps
u8    program_len || program
u8    software_len || software
```

`instance_id` persists on the client. Empty means the node mints one in Status; the client MUST store it. Strings ≤ 64 bytes. Protocol version is `major<<8|minor` (`0x0100` = 1.0). Cap `relay` marks a relay trunk. Cap `p2p` marks a client that will announce a listen port.

An old blob without proto/caps still decodes. Trailing unknown bytes MUST be ignored.

### 5.2 Status

Fields in order:

| field | encoding |
|-------|----------|
| code | u16be |
| lease_sec | u32be |
| mtu | u16be |
| vlan | u16be |
| keepalive_ms, idle_ms, reconnect_min_ms, reconnect_max_ms | u32be each |
| reconnect_fast_n | u16be |
| ip, gateway, instance_id | u8 len \|\| bytes |
| reconnect_fast_for_ms | u32be |
| selected proto, min, max | u16be each |
| caps | u64be |
| program, software | u8 len \|\| bytes |
| ticket | u16be len \|\| bytes |
| decoy | u8 n, then n × (u8 len \|\| bytes) |
| mask_pub | u8 len \|\| bytes (0 or 32) |
| replicas | u8 n, then n × (u8 len \|\| bytes) |
| dpi_flags | u8 |
| dpi_pad | u16be |
| roster | u16be len \|\| bytes |
| lists_serial | u64be |
| lists_hash | u8 len \|\| bytes (0 or 32) |
| peers | u8 (0 hub, 1 mesh, 2 vlan) |
| p2p | u8 (1 = node allows) |
| peer hints | u8 n ≤ 16, then n × (ip, host, port u16be, vlan u16be) |
| dns | u8 n ≤ 8, then n × (u8 len \|\| bytes) — resolver IPv4/IPv6 for the client TUN. Empty: the client MUST leave OS DNS unchanged. |
| ip6, gateway6 | optional tail: each u8 len \|\| bytes. Omitted entirely when both are empty (IPv4-only). A decoder that does not read them MUST ignore the trailing bytes. |

Selected protocol is the maximum of the intersection of the two ranges. Empty intersection yields code `protocol` (0x0203) and no mux.

### 5.3 Capability bits

| bit | name | meaning |
|-----|------|---------|
| 0 | multi | several instances on one key |
| 1 | resume | `ok_resumed` and lease hold |
| 2 | sticky_ip | reserved overlay IP on the key |
| 3 | vlan | VLAN from the key |
| 4 | rules | switch ACL |
| 5 | get | GET front |
| 6 | ws | WebSocket front |
| 7 | status | Hello/Status with code |
| 8 | timers | keepalive / idle / reconnect in Status |
| 9 | hello_caps | this cap block |
| 10 | reset | CmdReset |
| 11 | quic | QUIC mux front |
| 12 | dgram | CmdDgram |
| 13 | clockless | auth without client clock |
| 14 | ticket | resume ticket |
| 15 | decoy | Status lists origins |
| 16 | h2 | HTTP/2 CONNECT |
| 17 | h3 | HTTP/3 CONNECT |
| 18 | mask | borrowed origin certificate |
| 19 | relay | this session is a relay trunk |
| 20 | roster | Status may carry a signed roster |
| 21 | lists | Status may advertise list serial/hash |
| 22 | p2p | peer-to-peer overlay when mesh |
| 23 | dns | Status may carry DNS servers; gateway:53 is a resolver |
| 24 | connect | HTTP/2 CONNECT splice: TCP Open without inner mux |

A node that disables a front MUST clear that bit. A client that will not use a front MUST clear it. SOCKS, TUN, CDN vendor, OS, git, and origin hostnames are not caps.

Negotiate version: `max(intersection(client.min..client.max, node.min..node.max))`. Caps in Status are the node’s advertised set. The client decides which of those it uses.

---

## 6. Multiplex

After Status, each AEAD plaintext is a concatenation of frames:

```
u16be payload_length   0..16384
u8    cmd
u32be stream_id
bytes payload
```

Client stream identifiers are odd, starting at 1. Node identifiers are even, starting at 2. A live identifier MUST NOT be reused. After Close or Reset it MAY be reused.

| cmd | name | payload |
|-----|------|---------|
| 1 | Open | target |
| 2 | Data | bytes |
| 3 | Close | empty |
| 4 | Ping | 8 bytes, echoed |
| 5 | Pong | those 8 bytes |
| 6 | Pad | ignored |
| 7 | Reset | u16be code |
| 8 | Dgram | Open-target prefix then UDP payload |
| 9 | Leave | empty; stream_id MUST be 0 |

Open target: `u16be host_len || host || u16be port`. Dgram payload is that prefix plus the datagram.

Leave ends the mux session. stream_id is 0. Payload is empty. The node MUST close the session, Detach the overlay peer, and Release the lease (`Online=false`). `lease_hold` still keeps the IP for the same `instance_id`. Frames after Leave in the same batch MAY be ignored. An implementation that does not yet handle Leave MUST ignore the command (unknown mux commands MUST be ignored and MUST NOT reset the session). CmdClose ends one stream; it does not end the session.

Implementations MUST batch frames (about 1–16 KiB per AEAD plaintext). They MUST NOT drop Data under buffer pressure: they apply backpressure. Per-stream receive buffer is at least 64 KiB and at most 256 KiB. An Open that cannot be completed is Reset `stream_refused` (0x0501). Ping interval is `keepalive_ms`. Idle timeout is `idle_ms`. On `get`, Pad so plaintext before AEAD is about 400–600 bytes.

Length-prefixed wrappers (`h2`, `quic`, `h3`, `ctl`): `u32be length || payload`, at most 1 MiB. Mux payload inside AEAD remains ≤ 16384.

---

## 7. Identity and timers

A subscriber is a member key. An instance is a client-generated identifier stored locally. Empty instance: the node mints one in Status; the client persists it.

The same instance reconnecting receives `ok_resumed` and the same overlay IP. A second instance with `multi` off receives `auth_already_online`. With `multi` on, further instances receive pool addresses until `multi_limit` (default 8), then `auth_limit`. A sticky IP on the key is given to the first instance.

After disconnect, `lease_hold_ms` (default 45 s) keeps the IP for that instance. Leave (or a WebSocket close) Releases `Online` immediately; the hold is only the IP reservation for resume. A GET session that vanishes without Leave stays `Online` until the node next sees that session.

Defaults: handshake 10 s, idle 120 s, keepalive 15 s, lease hold 45 s, reconnect min 200 ms, max 8 s, fast 5 tries inside 8 s, then exponential backoff.

---

## 8. Status codes

Numeric values are stable. Names MAY grow. Numbers do not move.

| code | name | meaning |
|------|------|---------|
| 0x0000 | ok | new lease |
| 0x0001 | ok_resumed | same instance, same IP |
| 0x0101 | auth_bad_key | unknown key |
| 0x0102 | auth_revoked | key rotated or deleted |
| 0x0103 | auth_expired | join TTL |
| 0x0104 | auth_not_authorized | member disabled |
| 0x0105 | auth_limit | multi_limit |
| 0x0106 | auth_already_online | multi off, other instance |
| 0x0107 | auth_instance | bad instance id |
| 0x0108 | auth_clock | timestamp skew (unused if clockless) |
| 0x0201 | net_disabled | network off |
| 0x0202 | net_unknown | wrong network |
| 0x0203 | protocol | no common version |
| 0x0204 | rate_limit | too many handshakes |
| 0x0205 | timeout | handshake or idle |
| 0x0206 | internal | node fault |
| 0x0301 | ip_pool | pool exhausted |
| 0x0302 | ip_conflict | sticky clash |
| 0x0303 | vlan_denied | VLAN not allowed |
| 0x0401 | policy_drop | ACL or hub isolation |
| 0x0501 | stream_refused | Open failed |
| 0x0502 | stream_reset | peer Reset |

A non-OK Status forbids mux. HTTP 404 on GET auth is treated as `auth_bad_key`.

---

## 9. Overlay

The node loads a network document (CIDR, gateway, members, ACLs). Overlay IP and VLAN appear in Status. Open to an address inside the overlay CIDR is forwarded according to `peers`:

| peers | Overlay Open |
|-------|----------------|
| hub | Reset `policy_drop`. Internet Open is unchanged. |
| mesh | Splice to the peer session: Open `127.0.0.1:port` on that member. |
| vlan | As mesh, only if both ends share a VLAN. |

Hub splice: the receiving client accepts the stream and dials the local port.

`p2p` on the network defaults to on for mesh and vlan. When on, Status carries up to 16 peer hints (overlay IP, TLS-observed host, listen port, VLAN). A client with Hello cap `p2p` listens on TCP, announces with Open `loft.peer:<port>`, and MAY dial `host:port` with a 38-byte preface before falling back to hub splice:

```
"LFT2" || HMAC-SHA256(token, nonce || dest_port)[0:16] || nonce[16] || dest_port u16be
```

A client with `p2p: false` MUST use hub splice only. Observed host through a relay is the relay; a direct attempt then fails and hub splice is used.

ACL rules are first match, `accept` or `drop`. Empty rules still apply `peers`. Packet fields: source, destination, source VLAN, destination VLAN, to-gateway.

Overlay ICMP echo is CmdDgram to `dst:1` (IP protocol ICMP) with the IPv4 packet as payload. The node answers the gateway address. Other overlay destinations follow `peers` (hub drops member-to-member; mesh/vlan splice to that member). Echo to an address outside the overlay CIDR is emitted by the node as a real ICMP echo; the reply is returned as CmdDgram `session-ip:1`. A client answers echo for its own overlay IP locally and MUST NOT fabricate replies for other overlay or internet addresses. The node MUST treat the session overlay IP as the packet source (a Darwin utun dummy `169.254.8.1` is rewritten). The client MAY restore that dummy address on echo-reply so the kernel matches the ping socket.

---

## 10. Controller and relays

A controller holds keys, leases, ACLs, and signing. A relay terminates public TLS and copies plaintext onto a trunk session: Open `hub:1` or `hub:2`. Only a Hello with cap `relay` MAY Open `hub:*` or `ctl:1`. Member authentication and overlay IP remain on the controller.

```
client ──TLS──► relay ──trunk (mux hub:1|hub:2, ctl:1)──► controller
                     ▲
                     member key never stored on the relay
```

### 10.1 Roster

Ed25519 over the body:

```
u64be serial
u8 n
repeat n:
  id                 (u8+bytes)
  url                (u16be+bytes)
  decoy              (u8+bytes)
  fronts             u8 k, k × (u8+bytes), k ≤ 16
  drain              u8
  weight             u8 (0 means 100)
  path               (u8+bytes)
  mask_pub           u8 len, 0..32 bytes
```

Then `u8 pub_len || pub || u16be sig_len || sig`. Signature is Ed25519 over the body. Public key is 32 bytes. Signature is 64 bytes.

The live roster is `advertise` (id `self`) plus heartbeating relays that are not drain. Three missed beats (default interval 10 s) or a dead `ctl` stream remove the relay and increment serial.

Fetch after auth: Status `roster`. Fetch before auth: `GET {roster_path}` (default `/static/v3/manifest.json`), JSON `{"v":1,"p":"<base64url>"}`, `Cache-Control: public, max-age=60`. That GET succeeds on a CDN or a host with a real certificate. A decoy-spliced IP without session HMAC is forwarded to the origin. The document MUST NOT contain a member token. The client pins `controller_pub` if configured, otherwise the first valid public key.

### 10.2 Fleet (`ctl:1`)

Each message `u32be length || u8 type || body`.

| type | name | body |
|------|------|------|
| 1 | enrol_req | id, url, decoy, fronts |
| 2 | enrol_ok | signed slice |
| 3 | enrol_no | u16be code, reason |
| 4 | beat | load u8, conns u32be, program, uptime_s u32be |
| 5 | slice | signed slice |
| 6 | drain | id, u8 |

Slice (Ed25519 over the body): serial, id, url, decoy, fronts, drain, weight, beat_ms u32be, expires unix u64be, then pub and sig as in the roster. `enrol: list` requires the id in controller `relays:`. `enrol: open` accepts any authenticated trunk. YAML overrides URL, decoy, fronts, weight when set. Enrol deny: 1 unknown id, 2 bad form, 3 duplicate, 4 disabled.

Provisioning of machines and certificates is outside this protocol.

### 10.3 Address lists

The controller MAY publish named lists of addresses. Names have no protocol meaning. The client maps names to `direct`, `via`, or `drop`. If a host matches several mapped lists, apply drop, then direct, then via. Unmatched destinations use the client default (SHOULD be `via`).

Body (Ed25519, same key as the roster):

```
u64be serial
u8 nsets
repeat:
  name               (u8+bytes)
  u16be n
  repeat n: u8 kind, u16be+bytes value
```

Kind 1 exact domain, 2 suffix, 3 CIDR. Then public key and signature as in the roster.

Status carries `lists_serial` and SHA-256 of the signed blob. The client fetches `GET {lists_path}` (default `/static/v3/lists.json`) when serial or hash is newer. Operator YAML: a leading `.` or `*.` is suffix; a CIDR string is kind 3. Matching is case-insensitive for names. SOCKS matches hostname. TUN matches IP (CIDR).

---

## 11. Adversary

The construction is aimed at a path middlebox that classifies cheaply and drops. It is not a mixnet. Overlay IP, member key, and controller identity are secrets of the deployment, not of the public Internet.

### 11.1 Classification of the handshake

A ClientHello that looks like a current browser raises the cost of a JA3/JA4 rule that would also hit banks and portals. loft uses uTLS fingerprints in the reference client. A unique ALPN, a unique GREASE pattern, or a Go-default cipher list is a cheap tell. QUIC ALPN is `h3` for that reason.

`session_id` is 32 random-looking bytes. A middlebox that recomputes HKDF under a stolen member key can mark those ClientHellos. A middlebox without the key sees a string of the same length Chrome already sends.

Mask presents the origin certificate. CertificateVerify is Ed25519 under a key the origin does not use. A middlebox that completes TLS as a client, or that inspects CertificateVerify algorithm, can distinguish mask from the origin. Splice-only nodes avoid that tell.

### 11.2 Classification of the destination

An IP that serves only loft is cheap to drop. A CDN hostname, or an origin whose public site the operator is unwilling to drop, is expensive. Relays exist so that a classified IP can leave the roster while member identity stays on the controller.

A decoy-spliced probe sees the origin. Blocking the decoy hostname or the origin IP is a political and operational decision of the path, not a protocol constant.

### 11.3 Active probe

A scanner that opens TLS without a valid `session_id` is spliced. It receives the origin’s certificate and bytes. A scanner that possesses a member key is a member.

GET without a valid Cookie returns 404 with HTML. Cover paths (roster, lists, static assets) are ordinary GET.

### 11.4 Replay and clocks

Auth `0x02` is a nonce plus HMAC. The node refuses a nonce it has seen inside the cache TTL. The client clock is unused, so a skewed laptop still joins. Tickets carry expiry on the issuer’s clock so replicas can resume without sharing the nonce cache.

### 11.5 Roster and lists

The signed document is public. Anyone who can GET the CDN path can read live fronts. That is the same surface as a public website. The member key is absent. A client that pins `controller_pub` rejects a substituted key. Trust-on-first-use accepts the first valid key and pins it thereafter.

### 11.6 Overlay

Hub isolation Reset `policy_drop` on member-to-member Open. Mesh splices through the node. P2P is an optional TCP attempt using the TLS-observed host; NAT and relays make that attempt fail, after which hub splice is used. Peer hints reveal overlay IP, observed host, and listen port to other members of the same network.

### 11.7 What this document does not claim

Anonymity among members. Resistance to a middlebox that already has the member key. Resistance to a middlebox that blocks every unknown IP. Resistance to a terminator of TLS that is itself adversarial and also holds the inner key. Hardware bypass of an in-path appliance. Those are outside the protocol.

---

## 12. Limits

| limit | value |
|-------|-------|
| mux payload | ≤ 16384 |
| Hello/Status strings | ≤ 64 |
| roster URL | ≤ 255 |
| inflight GET | ≤ 8 |
| streams per session | SHOULD ≤ 256 |
| AEAD batch | ≤ 48 KiB (`ws`), ≤ header budget (`get`) |
| GET cover plaintext | about 400 bytes before AEAD |
| length-prefixed wrapper | ≤ 1 MiB |
| peer hints | ≤ 16 |
| roster entries | ≤ 255 |
| fronts per entry | ≤ 16 |
| nonce cache | TTL 10 min; sweep above 50 000 entries |
| dpi_pad | 0–2048 |

---

## 13. Versioning

Protocol 1.0 remains 1.0 until the first release. Major bump: incompatible framing or auth. Minor bump: new caps; old clients still speak. Handshake selects the highest version in both ranges.

Software version is a string on Hello and Status. Git hashes are omitted. Program name and semver are enough on the wire.

A client MUST implement at least `get` or `ws`, mux, and auth. Roster fetch, decoy, mask, lists, and overlay forwarding are required of a client that will run on a filtered path or in a meshed network.

---

## 14. Test vectors

Data, stream 1, payload `hi`:

```
00 02 02 00 00 00 01 68 69
```

Open `example.com:443` (payload of CmdOpen):

```
00 0b 65 78 61 6d 70 6c 65 2e 63 6f 6d 01 bb
```

Reset `stream_refused`: payload `05 01`.

Member key shape: `lt_` + 48 lowercase hex. SHA-256 of the whole string is what the node stores.

---

## 15. References

1. RFC 2119. Key words for use in RFCs to Indicate Requirement Levels.
2. RFC 5869. HMAC-based Extract-and-Expand Key Derivation Function (HKDF).
3. RFC 7539 / RFC 8439. ChaCha20 and Poly1305 for IETF Protocols.
4. RFC 8032. Edwards-Curve Digital Signature Algorithm (Ed25519).
5. RFC 8446. The Transport Layer Security (TLS) Protocol Version 1.3.
6. RFC 9000. QUIC: A UDP-Based Multiplexed and Secure Transport.
7. RFC 9113. HTTP/2.
8. RFC 9114. HTTP/3.
9. RFC 6455. The WebSocket Protocol.
10. RFC 1928. SOCKS Protocol Version 5.

---

## 16. Conclusion

The inner protocol is small: one AEAD, one mux, stable codes, a signed roster, named address lists, and an overlay policy. Diversity is pushed to the path (front, decoy origin, relay IP) so that a block must be re-aimed each time the path changes, while member identity and overlay address remain.
