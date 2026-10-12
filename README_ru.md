# loft

[English](README.md) · **Русский**

**loft** — Layered Overlay For TLS.  
**LFTP** — LOFT Tunneling Protocol, версия **1.0**.

Приватный оверлей в форме обычного HTTPS. Это дерево — спецификация и эталонная нода плюс клиенты.

ПО `0.1.0`. Протокол `1.0`.

### Почему loft?

Можно взять расшифровку, можно не брать:

- **L**ayered **O**verlay **F**or **T**LS
- **L**ow-**O**bservability **F**orwarding **T**LS
- **L**ightweight **O**verlay **F**ront **T**ransport

**LFTP** — **LOFT** **T**unneling **P**rotocol, в одном ряду с PPTP, L2TP и SSTP.

Слово — чердак: этаж над обычным HTTPS. Остальное необязательно.

## Протокол

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
| [docs/protocol_ru.md](docs/protocol_ru.md) | Провод. По этому файлу можно писать стороннюю реализацию. |
| [docs/architecture_ru.md](docs/architecture_ru.md) | Как уложено это дерево. |
| [docs/guide_ru.md](docs/guide_ru.md) | Сборка, конфиг, запуск, Debian. |
| [docs/yandex-cdn_ru.md](docs/yandex-cdn_ru.md) | Yandex Cloud CDN: инструкция оператора. |
| [docs/keenetic_ru.md](docs/keenetic_ru.md) | Клиент KeeneticOS (Entware, OpkgTun). Каталог: [clients/keenetic/](clients/keenetic/). |

## Репозиторий

```
docs/          протокол, архитектура, гайд
internal/      провод, mux, crypto, транспорт, оверлей
server/        loft-node (controller или relay)
clients/go        loft, loft-front, loft-probe
clients/macOS     меню-бар (запускает loft-front)
clients/keenetic  Entware / OpkgTun (план и макет)
brand/            марка и иконки
```

## Сборка

Go 1.24+. Из корня репозитория:

```bash
make test
make node      # ./loft-node
make client    # dist/clients/go/loft
make macos     # dist/clients/macOS/loft.app
make macos-dmg # dist/clients/macOS/loft-*-macos-<arch>.dmg
make dist      # tarball/.deb ноды + архивы CLI
```

Тег `vX.Y.Z` (должен совпасть с `VERSION`) гоняет GitHub Actions: тесты, `make dist`, macOS DMG (ad-hoc) и Release. Вручную: Actions → Dist.

```bash
export LOFT_TOKEN='change-me-long-random'
./loft-node -c server/configs/server.yaml -dir /var/lib/loft
./dist/clients/go/loft -c clients/go/configs/client.yaml
ALL_PROXY=socks5://127.0.0.1:1080 curl https://example.com
```

Пакет Debian и установщик: [server/debian/README_ru.md](server/debian/README_ru.md).

## Лицензия

[Apache License 2.0](LICENSE). LFTP 1.0 можно реализовать по [docs/protocol_ru.md](docs/protocol_ru.md) без платы.
