# loft · марка

[English](README.md) · **Русский**

Сборка и запуск: [docs/guide_ru.md](../docs/guide_ru.md). Протокол: [docs/protocol_ru.md](../docs/protocol_ru.md).

Марка — L-крыша с лампой: буква L, чердак оверлея над HTTPS, зажжённое окно, когда туннель жив.

Сплошные заливки только на марке и в трее. Лёгкое свечение окна — только на иконке приложения.

## Палитра

| Токен | Hex | Где |
|-------|-----|-----|
| Ink | `#1A1714` | фон иконки приложения, марка на светлом |
| Stone | `#B7A99A` | тело L на тёмной иконке |
| Copper | `#C67B3A` | трей Windows |
| Lamp | `#E8B84A` | окно, подключено |
| Lamp hi | `#F3D27A` | блик окна на иконке приложения |
| Snow | `#F3EDE4` | марка на тёмном, трей на тёмном |
| Paper | `#E8DFD2` | поля, превью |
| Mute | `#8A7E72` | подписи |

## Шрифт

**Avenir Next** (Linotype, Adrian Frutiger / Akira Kobayashi, 2004). Геометрический гуманистический гротеск: круглая «o» рифмуется с окном лампы.

| Роль | Начертание | Правило |
|------|------------|---------|
| Вордмарк / lockup | Demi 600 | строчные `loft`, трекинг −0.04em |
| Заголовки UI | Demi 600 | sentence case, трекинг −0.02em |
| Кнопки, вкладки | Medium 500 | без капса, кроме коротких статусов |
| Текст, документация | Regular 400 | межстрочный 1.45 |
| Код, ключи `lt_…` | SF Mono / Menlo / Consolas | никогда Avenir |

Ultra Light, Heavy и курсив в lockup не использовать.

Стек: `"Avenir Next", Avenir, "Helvetica Neue", "Segoe UI", ui-sans-serif, system-ui, sans-serif`.

На macOS шрифт системный и с кириллицей. На Windows те же пиксели вордмарка — SVG-контуры или установленный файл шрифта.

## Файлы

| Файл | Назначение |
|------|------------|
| `svg/loft-mark.svg` | протокол, README, сайт |
| `svg/loft-lockup.svg` | марка + слово *loft* |
| `png/type-specimen.png` | таблица шрифта |
| `svg/loft-icon.svg` | иконка приложения (тёмная) |
| `svg/loft-icon-light.svg` | иконка на светлом |
| `svg/loft-favicon.svg` | 32px |
| `svg/loft-menubar-template.svg` | шаблон macOS `NSStatusItem` |
| `app/loft.icns` | бандл macOS |
| `app/loft.ico` | приложение Windows |
| `tray/loft-tray-16.png` | notify Windows, подключено |
| `tray/loft-tray-off-16.png` | notify Windows, отключено |
| `menubar/loftTemplate.png` (`@2x` `@3x`) | меню-бар macOS, подключено |
| `menubar/loftTemplate-off-*.png` | меню-бар macOS, отключено |

## Правила

1. Крыша закрывает только ножку L; ступня уходит вправо. Это L.
2. При ≤16px — сетка `PIX16` в `generate.py`. Не уменьшать с 1024.
3. Меню-бар macOS — **template image** (чёрное + альфа). Система красит сама.
4. Трей Windows по умолчанию — **copper + lamp**.
5. Подключено: лампа горит. Отключено: то же L, окно погашено (macOS: без выреза).
6. Слово *loft* — строчными, Avenir Next Demi, трекинг −0.04em.

Пересборка: `/tmp/loft-brand/bin/python generate.py`
