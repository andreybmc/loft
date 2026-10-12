# loft

**English** · [Русский](README_ru.md)

**loft** — Layered Overlay For TLS.  
**LFTP** — LOFT Tunneling Protocol, version **1.0**.

A private overlay that uses the on-wire shape of ordinary HTTPS. This tree is the specification and a reference node plus clients.

Software `0.1.0`. Protocol `1.0`.

### Why loft?

Pick an expansion, or skip them:

- **L**ayered **O**verlay **F**or **T**LS
- **L**ow-**O**bservability **F**orwarding **T**LS
- **L**ightweight **O**verlay **F**ront **T**ransport

**LFTP** is **LOFT** **T**unneling **P**rotocol, in the same row as PPTP, L2TP, and SSTP.

The word is an attic: a floor above ordinary HTTPS. The rest is optional.

## Protocol

```
application → SOCKS5 / TUN
    mux (Open / Data / Close / Dgram / Leave)
      ChaCha20-Poly1305
        get | ws | h2 | quic | h3
          TLS 1.3
            TCP 443 or UDP 443
```

| | |
|--|--|
| [docs/protocol.md](docs/protocol.md) | Wire. A third party can implement from this file. |
| [docs/architecture.md](docs/architecture.md) | How this tree is laid out. |
| [docs/guide.md](docs/guide.md) | Build, configure, run, Debian. |
| [docs/yandex-cdn_ru.md](docs/yandex-cdn_ru.md) | Yandex Cloud CDN operator guide (RU). |
| [docs/keenetic.md](docs/keenetic.md) | KeeneticOS Entware client (OpkgTun). Catalog: [clients/keenetic/](clients/keenetic/). |

## Repository

```
docs/          protocol, architecture, guide
internal/      wire, mux, crypto, transport, overlay
server/        loft-node (controller or relay)
clients/go        loft, loft-front, loft-probe
clients/macOS     menu-bar app (launches loft-front)
clients/keenetic  Entware / OpkgTun (plan and mock)
brand/            mark and icons
```

## Build

Go 1.24+. From the repository root:

```bash
make test
make node      # ./loft-node
make client    # dist/clients/go/loft
make macos     # dist/clients/macOS/loft.app
make macos-dmg # dist/clients/macOS/loft-*-macos-<arch>.dmg
make macos-dmgs # both arm64 and x86_64 DMGs (CI)
make dist      # node tarballs/.deb + CLI archives
```

Tag `vX.Y.Z` (must match `VERSION`) runs GitHub Actions: tests, `make dist`, macOS DMGs arm64 and x86_64 (ad-hoc), and a Release. Manual: Actions → Dist.

```bash
export LOFT_TOKEN='change-me-long-random'
./loft-node -c server/configs/server.yaml -dir /var/lib/loft
./dist/clients/go/loft -c clients/go/configs/client.yaml
ALL_PROXY=socks5://127.0.0.1:1080 curl https://example.com
```

Debian package and installer: [server/debian/README.md](server/debian/README.md).

## License

[Apache License 2.0](LICENSE). LFTP 1.0 may be implemented from [docs/protocol.md](docs/protocol.md) without a fee.
