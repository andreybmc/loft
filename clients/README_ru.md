# Клиенты

[English](README.md) · **Русский**

| | |
|--|--|
| [`go/cmd/loft`](go/cmd/loft/) | CLI: SOCKS5, TUN. Эталон протокола 1.0 |
| [`go/cmd/loft-front`](go/cmd/loft-front/) | Хелпер сессии. Его запускает macOS-приложение |
| [`go/cmd/loft-probe`](go/cmd/loft-probe/) | ICMP/TCP/UDP/DNS/HTTP через сессию |
| [`go/cmd/loft-bench`](go/cmd/loft-bench/) | Автозамер GET/WS против пути connect на macOS |
| [`macOS/`](macOS/) | Меню-бар. UX: [`macOS/design/DESIGN_ru.md`](macOS/design/DESIGN_ru.md) |
| [`keenetic/`](keenetic/) | Entware / OpkgTun. План: [`keenetic/PLAN_ru.md`](keenetic/PLAN_ru.md). Макет: [`keenetic/design/index.html`](keenetic/design/index.html) |

Общий код: `internal/`. Артефакты: `dist/clients/`.

```bash
make client    # dist/clients/go/loft
make front     # dist/clients/go/loft-front
make probe     # dist/clients/go/loft-probe
make bench     # dist/clients/go/loft-bench
make macos     # dist/clients/macOS/loft.app
make macos-dmg # dist/clients/macOS/loft-*-macos-<arch>.dmg
make macos-dmgs # оба DMG, arm64 и x86_64 (CI)
make dist-clients # dist/clients/go/loft-<ver>-<os>-<arch>.tar.gz
```

Провод: [docs/protocol_ru.md](../docs/protocol_ru.md). Запуск: [docs/guide_ru.md](../docs/guide_ru.md).
