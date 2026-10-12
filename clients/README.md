# Clients

**English** · [Русский](README_ru.md)

| | |
|--|--|
| [`go/cmd/loft`](go/cmd/loft/) | CLI: SOCKS5, TUN. Protocol 1.0 reference |
| [`go/cmd/loft-front`](go/cmd/loft-front/) | Session helper. The macOS app launches this |
| [`go/cmd/loft-probe`](go/cmd/loft-probe/) | ICMP/TCP/UDP/DNS/HTTP through a session |
| [`go/cmd/loft-bench`](go/cmd/loft-bench/) | Automated GET/WS throughput vs the macOS connect path |
| [`macOS/`](macOS/) | Menu-bar app. UX: [`macOS/design/DESIGN.md`](macOS/design/DESIGN.md) |
| [`keenetic/`](keenetic/) | Entware / OpkgTun. Plan: [`keenetic/PLAN.md`](keenetic/PLAN.md). Mock: [`keenetic/design/index.html`](keenetic/design/index.html) |

Shared code: `internal/`. Artifacts: `dist/clients/`.

```bash
make client    # dist/clients/go/loft
make front     # dist/clients/go/loft-front
make probe     # dist/clients/go/loft-probe
make bench     # dist/clients/go/loft-bench
make macos     # dist/clients/macOS/loft.app
make macos-dmg # dist/clients/macOS/loft-*-macos-<arch>.dmg
make macos-dmgs # both arm64 and x86_64 DMGs (CI)
make dist-clients # dist/clients/go/loft-<ver>-<os>-<arch>.tar.gz
```

Wire: [docs/protocol.md](../docs/protocol.md). Run: [docs/guide.md](../docs/guide.md).
