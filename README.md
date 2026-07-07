# Lotsman

**Самовосстанавливающийся control-plane для домашнего анти-цензурного роутера** (OpenWrt /
NanoPi R5S). Следит за сервисами, прозванивает их по реальному пути LAN-клиента и
**сам переключает способ обхода** — ZAPRET (DPI-десинк через nfqws), зарубежный VPN-узел
или direct — когда что-то перестаёт работать. Роутер чинит себя сам, без ручных
`blockcheck` и правок конфига.

Сделан под российский интернет после 2024: цензор (ТСПУ) всё чаще **троттлит по IP
назначения** (молча морозит зарубежные потоки после ~16 КБ), а не рвёт по SNI — а это
режим, который одним DPI-десинком не лечится. Lotsman ловит такую заморозку из живой
таблицы соединений и сам уводит затронутый сервис на зарубежный egress.

> Личный, оборонительный, анти-цензурный инструмент для собственной сети. Работает поверх
> [sing-box](https://github.com/SagerNet/sing-box) и [zapret](https://github.com/bol-van/zapret).

---

## Содержание

- [Что умеет](#что-умеет)
- [Требования](#требования)
- [Быстрый старт](#быстрый-старт)
- [Конфигурация](#конфигурация) — сервисы, цепочки, пулы, подписки, hostlists, …
- [lotsmanctl — инструмент настройки](#lotsmanctl--инструмент-настройки)
- [Флаги lotsmand](#флаги-lotsmand)
- [Архитектура](#архитектура)
- [Благодарности](#благодарности)
- [Лицензия](#лицензия)

---

## Что умеет

- **Прозвон реального пути.** Трафик самого роутера не проходит через tproxy sing-box,
  поэтому Lotsman прозванивает *через* SOCKS-inbound sing-box — меряет ровно то, что
  видит LAN-клиент (VPN / direct+nfqws), а не маршрут самого роутера.
- **Само-восстановление.** У каждого сервиса — цепочка фолбэков (`PREFERRED → ALT_ZAPRET →
  VPN → EMERGENCY`); при отказе сервис эскалирует на следующий рабочий тир и тихо
  перепрозванивает нижние, чтобы вернуться, когда они оживут. Асимметричные пороги (уходим
  быстро, возвращаемся медленно), анти-флап демпфер, окна устаканивания.
- **Детект IP-троттла, а не только жёсткого отказа.** Пассивный stall-детектор смотрит на
  дельты байт между проходами: поток, замёрзший в середине на нескольких КБ (сигнатура
  заморозки ТСПУ, TCP или UDP), уводит сервис на зарубежный egress, до которого
  IP-троттл не дотянется.
- **Учится, что работает.** База знаний ранжирует `(сервис, стратегия)` по
  recency-взвешенному (EWMA) успеху с членом исследования (discounted-UCB) — после отказа
  выбирает вероятно-рабочую альтернативу и ре-валидирует устаревшие.
- **Ремедиации без разрыва соединений.** reject-QUIC / IP-fallback применяются через
  hot-reload rule-set'ов sing-box (без рестарта), а рестарт sing-box откладывается, пока
  есть живой voice/RTC-поток, чтобы не порвать активный звонок.

---

## Требования

- Роутер с **sing-box** в качестве data-plane (включены `clash_api` и SOCKS-inbound) и
  **zapret/nfqws** для DPI-десинка. Проверено на NanoPi R5S / OpenWrt.
- **Go 1.26+** для сборки (один статический ARM64-бинарь, без CGO).

---

## Быстрый старт

```sh
# 1. Сборка под роутер (aarch64):
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o bin/lotsmand-arm64  ./cmd/lotsmand
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o bin/lotsmanctl-arm64 ./cmd/lotsmanctl

# 2. Свой конфиг (старт — с комментированного примера):
cp examples/r5s.yaml /etc/lotsman/r5s.yaml      # затем впишите: subscriptions, pools, services
#    examples/ — только плейсхолдеры. Реальные URL подписок / креды узлов кладите СЮДА,
#    в конфиг на роутере, а НЕ в репозиторий.

# 3. Залить бинари + init-скрипт на роутер:
scp bin/lotsmand-arm64  root@192.168.1.1:/opt/lotsman/lotsmand
scp bin/lotsmanctl-arm64 root@192.168.1.1:/usr/bin/lotsmanctl
scp examples/lotsmand.init root@192.168.1.1:/etc/init.d/lotsman
scp examples/lotsmand.env  root@192.168.1.1:/opt/lotsman/lotsmand.env   # переопределение флагов

# 4. Сначала только наблюдаем (dry-run — логирует решения, ничего не меняет):
#    examples/lotsmand.init по умолчанию с -dry-run=1. Запускаем и смотрим:
ssh root@192.168.1.1 '/etc/init.d/lotsman enable && /etc/init.d/lotsman start; logread -f | grep lotsmand'

# 5. Когда доверяете — меняем -dry-run=0 в /opt/lotsman/lotsmand.env и рестарт.
```

Перед подменой боевого конфига sing-box — всегда валидируйте сгенерированный:

```sh
lotsmanctl generate -config /etc/lotsman/r5s.yaml -out /tmp/sb.json && sing-box check -c /tmp/sb.json
```

---

## Конфигурация

Полностью прокомментированные примеры — [`examples/r5s.yaml`](examples/r5s.yaml) и
[`examples/config.yaml`](examples/config.yaml). Ниже — разбор по секциям.

> ⚠️ **Никогда не коммитьте реальные URL подписок и креды узлов.** Держите их только в
> `/etc/lotsman/r5s.yaml` на роутере. В репозитории — только плейсхолдеры.

### Сервис (`services`)

Сервис описывает, **какой трафик** он ловит (по `rule_sets` / `domains` / `ips`) и **как
его маршрутизировать** — через цепочку фолбэков `chain`.

```yaml
services:
  - name: youtube
    probe_target: https://www.youtube.com/generate_204   # что прозванивать
    rule_sets: [geosite-youtube]        # матч по rule-set'ам sing-box
    domains:   [googlevideo.com, ytimg.com]  # инлайн domain_suffix
    sticky: true                        # не флапать с рабочего узла
    profile: streaming                  # веса качества узла для выбора
    priority: 0                         # порядок правил (меньше = раньше)
    chain:                              # лесенка: лучшее → последний резерв
      - { state: PREFERRED,  class: zapret, strategy_id: alt12 }  # десинк, трафик direct (низкий пинг)
      - { state: ALT_ZAPRET, class: zapret }                      # strategy_id пусто = выберет KB
      - { state: VPN,        class: vpn,       strategy_id: vpn_url_test_udp }
      - { state: EMERGENCY,  class: emergency, strategy_id: emergency_pool }
```

**Поля сервиса:**

| Поле | Тип | Назначение |
|---|---|---|
| `name` | string | Уникальное имя сервиса. |
| `probe_target` | string | HTTP: полный URL; TCP/STUN: `host:port`. |
| `probe_type` | string | `http` (деф.) / `tcp` / `stun`. `quic` — только на уровне рунга (см. ниже). |
| `rule_sets` | []string | Тэги rule-set'ов sing-box (`geosite-youtube`). |
| `domains` | []string | Инлайн `domain_suffix`-матчи. |
| `ips` | []string | Инлайн `ip_cidr` (для UDP/voice без SNI — напр. блок voice-серверов Discord). |
| `ips_file` | string | Файл с доп. CIDR'ами (по одному в строке; `#`-комменты ок; отсутствие файла — не ошибка). |
| `sticky` | bool/unset | `*bool`: unset = наследовать категорию; `true` = не флапать; `false` = балансировать по латентности. |
| `profile` | string | `general` / `voice` / `streaming` / `gaming` — веса выбора узла. |
| `priority` | int | Порядок эмиссии route-правил: **меньше = раньше** (sing-box first-match). Спец-правила в минус, catch-all в плюс. |
| `escalate_after` | int | Сколько провалов пробы до эскалации (0 = глобальный дефолт). |
| `exclude_domains` | []string | Передаётся в nfqws-композитор как `--hostlist-exclude-domains` (десинк ОТМЕНЯЕТСЯ для этих доменов — CDN, что ломаются под десинком). **Маршрут не меняется.** |
| `spread_clients` | []string | CIDR'ы LAN-клиентов, размазанные по узлам VPN-пула (rendezvous-hash, LOT-23). |
| `chain` | []step | Цепочка фолбэков (если пусто — наследуется из категории). |

**Рунг цепочки (`chain[]`):**

| Поле | Значения | Назначение |
|---|---|---|
| `state` | `PREFERRED` `ALT_ZAPRET` `VPN` `EMERGENCY` `BROKEN` `LOCKED` | Метка рунга; эскалация слева-направо при провале. |
| `class` | `zapret` `vpn` `emergency` `direct` | Класс стратегии. |
| `strategy_id` | string | Для `zapret`: id из каталога (`alt12`) или пусто = KB выберет. Для `vpn`/`emergency`: имя пула. Для `direct`: `direct`. |
| `probe_type` | `http` `tcp` `stun` `quic` | Пер-рунг override пробы (LOT-3). **`quic` только на direct/zapret-рунгах** (box-direct H3; на VPN-рунге даст ложный фейл — валидатор отвергнет). |
| `probe_target` | string | Пер-рунг override цели пробы. |

### Подписки (`subscriptions`)

```yaml
subscriptions:
  - { name: main,   url: "https://YOUR-PANEL.example/s/TOKEN", format: auto, tags: [normal], enabled: true }
  # одиночный узел прямо в URL (формат single_url — url И ЕСТЬ узел, без фетча):
  - { name: inline-hy2, url: "hysteria2://PASSWORD@HOST:443?sni=SNI&obfs=salamander", format: single_url, tags: [normal], enabled: true }
  - { name: backup, url: "https://YOUR-PANEL.example/s/EMERGENCY", format: clash, tags: [emergency], enabled: true }
```

`format`: `auto` (сниффит) / `clash` / `singbox` / `v2ray_base64` / `v2ray_plaintext` /
`single_url`. `tags` группируют узлы для фильтров пулов. `enabled: false` — не фетчить.

### Пулы (`pools`)

Пул — это `url_test`-группа узлов, отфильтрованных по возможностям / тэгам / стране.

```yaml
pools:
  vpn_url_test:                  # TCP-пул (VLESS и т.п.)
    type: url_test
    interval: 1m
    filter:
      caps: [tcp]
      tags_exclude: [emergency]
      countries_exclude: [ru]    # держим RF-блокнутые сервисы подальше от RU-выходов
  vpn_url_test_udp:              # UDP-нативный пул (hy2/tuic) для voice/QUIC
    type: url_test
    warmup: true                 # всегда «горячий» (idle_timeout→0s) для мгновенного failover
    filter:
      caps: [udp_native]
      tags_exclude: [emergency]
  emergency_pool:
    type: url_test
    filter: { tags_include: [emergency] }
```

`filter`: `caps` (`tcp` / `udp_native`, AND), `tags_include` (OR), `tags_exclude` (NOT),
`countries_include`/`countries_exclude` (ISO-2, строчными; узлы без флага в имени = страна
неизвестна → `countries_exclude` их НЕ режет). `warmup: true` форсит непрерывный прозвон.

### Категории (`categories`)

Семь встроенных (`ru_direct`, `streaming`, `messaging`, `gaming`, `dev_tools`, `iot_check`,
`generic`); конфиг может переопределить/добавить. Сервис указывает `category:` и наследует
`default_chain`, `profile`, `sticky`, `required_caps`.

```yaml
categories:
  streaming:
    required_caps: [tcp]
    profile: streaming
    sticky: true
    default_chain:
      - { state: PREFERRED,  class: zapret }
      - { state: VPN, class: vpn, strategy_id: vpn_url_test }
      - { state: EMERGENCY, class: emergency, strategy_id: emergency_pool }
```

### Hostlists (автообновляемые списки доменов)

Демон периодически (`-check-interval`) тянет `sources`, выкидывает `exclude`, дедуплицирует
и атомарно пишет в `out` — файл, который читает nfqws.

```yaml
hostlists:
  - name: gaming-zapret
    out: /opt/zapret-lotsman/lists/gaming.txt
    sources:
      - https://example.invalid/gaming-launchers.txt
    exclude:
      - https://example.invalid/whitelist-cdn.txt
    min_keep_ratio: 0.5    # не писать, если список усох ниже 50% прошлого хорошего (защита от пустого зеркала)
```

### Прочие секции

```yaml
devices:                          # пер-клиентский override по IP/CIDR LAN
  - { name: gaming-pc, sources: [192.168.1.50], policy: direct }   # policy: direct | block | <имя пула>

zapret:                           # nfqws-инстансы (один = глобальный, много = пер-правило)
  instances:
    - name: nfqws_0
      qnum: 100
      capture: { tcp: [80, 443], udp: [53, "50000-50100"] }

fakeip:                           # маршрутизация по синтетическим IP вместо SNI
  enabled: true
  inet4_range: 198.18.0.0/15
  resolver: https://1.1.1.1/dns-query

subscription_via_pool: vpn_url_test   # тянуть подписки через VPN-пул (если зеркала флапают; нужен -probe-proxy)
utls_fingerprint: chrome              # uTLS-отпечаток по умолчанию для TLS-outbound'ов
singbox_version: "1.12.17"            # гейтит версия-специфичные фичи генератора
```

### Подводные камни

- **`exclude_domains` не меняет маршрут** — только отменяет nfqws-десинк для доменов
  (для CDN, что работают raw, но ломаются под десинком).
- **`sticky` — указатель**: `unset` ≠ `false`. unset наследует категорию; явный `false`
  переопределяет `sticky: true` категории.
- **QUIC-проба только на direct/zapret-рунгах** — на VPN/emergency валидатор отвергнет.
- **`warmup: true` форсит `idle_timeout: 0s`** — пул не засыпает (важно для voice/QUIC).

---

## lotsmanctl — инструмент настройки

`lotsmanctl` — операторский CLI: генерация/слияние конфига sing-box из YAML + живых
подписок, проверка когерентности маршрутизации и мониторинг живого демона. У каждой
подкоманды — свои флаги. Безопасность по умолчанию: всё либо локально/dry-run, либо
read-only на боевом.

### `generate` — собрать конфиг sing-box с нуля

Генерирует полный `config.json` sing-box из YAML + живых подписок. **Локально, read-only.**

```sh
lotsmanctl generate -config examples/r5s.yaml -out /tmp/sb.json
sing-box check -c /tmp/sb.json    # обязательно проверить перед подменой
```

| Флаг | Деф. | Назначение |
|---|---|---|
| `-config` | (обяз.) | Путь к YAML-конфигу. |
| `-out` | stdout | Куда писать `config.json` (пусто = stdout). |
| `-clash-listen` | `127.0.0.1:9090` | Адрес clash-api в генерируемом конфиге. |
| `-tproxy-port` | `7893` | Порт tproxy-inbound. |
| `-socks-probe` | (выкл) | host:port socks probe-in inbound. |

### `merge` — вставить узлы подписок в СУЩЕСТВУЮЩИЙ конфиг

Хирургически вставляет узлы из подписок в боевой, вручную настроенный `config.json` sing-box,
обновляя пул селектора и НЕ затрагивая остальное (inbounds, route, DNS, ручные узлы). Один
url-test-пул на подписку. **Локально, read-only на вход.**

```sh
lotsmanctl merge -config r5s.yaml -singbox-config /etc/sing-box/config.json -out /tmp/sb.json -selector vpn
sing-box check -c /tmp/sb.json
```

| Флаг | Деф. | Назначение |
|---|---|---|
| `-config` | (обяз.) | YAML с подписками. |
| `-singbox-config` | (обяз.) | Существующий `config.json` для слияния. |
| `-out` | stdout | Куда писать результат. |
| `-selector` | `vpn` | Тэг селектора, в который вставить пулы. |
| `-probe-url` | `https://www.gstatic.com/generate_204` | URL health-check'а пулов. |
| `-interval` | `5m` | Период url-test'а. |
| `-udp-mode` | `primary-fallback` | `unified` / `split` / `primary-fallback` — как разводить UDP. |

### `status` — здоровье живого демона

Снимает Prometheus-метрики демона + опрашивает clash-api: по каждому сервису — позиция/тир,
флаг broken, leak/dead-ratio, выбранный узел, число фейлов. **Read-only.**

```sh
lotsmanctl status
lotsmanctl status -metrics-addr 192.168.1.1:9101 -clash-base http://192.168.1.1:9090
```

| Флаг | Деф. | Назначение |
|---|---|---|
| `-metrics-addr` | `127.0.0.1:9101` | Адрес метрик демона. |
| `-clash-base` | `http://127.0.0.1:9090` | Clash-API (для опроса селекторов). |
| `-clash-secret` | (нет) | Секрет clash-api, если включён. |

### `scan` — что НЕ работает прямо сейчас (с роутера)

Прозванивает инлайн-домены каждого сервиса по живому пути роутера (через боевой nfqws) и
говорит, что достижимо, а что заблокано. TCP для всех; QUIC дополнительно для streaming.
**Запускать НА РОУТЕРЕ, read-only.**

```sh
lotsmanctl scan -config /etc/lotsman/r5s.yaml
lotsmanctl scan -config r5s.yaml -timeout 10s -concurrency 16
```

| Флаг | Деф. | Назначение |
|---|---|---|
| `-config` | (обяз.) | YAML-конфиг. |
| `-timeout` | `6s` | Таймаут на домен. |
| `-concurrency` | `8` | Параллельных проб. |

### `doctor` — когерентность sing-box ↔ nfqws

Показывает, какие домены zapret-сервисов реально доходят до nfqws и где дыры (домены из
`.srs` rule-set'ов, которые nfqws не читает; матчи по `ip_cidr`; пересечения с exclude-листом
nfqws). **Локально, диагностика.**

```sh
lotsmanctl doctor -config r5s.yaml
lotsmanctl doctor -config r5s.yaml -nfqws-exclude /opt/flowseal-current/lists/list-exclude.txt
```

### `harvest` — добыть стратегии zapret через blockcheck

Гоняет `blockcheck.sh` (по умолчанию SIMULATE=1, безопасно) и импортирует найденные
nfqws-стратегии в каталог — забираем курированный список стратегий zapret, не выводя его
заново. **По умолчанию ничего не трогает; `-simulate=false` лезет в живой nfqws/сеть.**

```sh
lotsmanctl harvest -domains rutracker.org -scanlevel force -out strategies.json
lotsmanctl harvest -domains discord.com -target-class discord_tcp -out strategies.json
```

| Флаг | Деф. | Назначение |
|---|---|---|
| `-script` | `/opt/zapret/blockcheck.sh` | Путь к blockcheck.sh. |
| `-domains` | `rutracker.org` | Целевые домены (через запятую). |
| `-scanlevel` | `force` | `quick` / `standard` / `force` — широта поиска. |
| `-simulate` | `true` | SIMULATE=1, без сети/nfqws (безопасно). |
| `-out` | stdout | Слить найденное в JSON-каталог. |
| `-target-class` | (нет) | Метка класса (`discord_tcp`, `quic`, `games`…) для пер-сервисной композиции. |

---

## Флаги lotsmand

| Флаг | Назначение |
|---|---|
| `-config` | YAML-конфиг (сервисы/подписки/пулы/zapret/devices). |
| `-dry-run` | Логировать действия вместо выполнения (деф. true — начинайте отсюда). |
| `-simulate` | Скриптовый прозвон вместо реального (локальное демо). |
| `-interval` | Период пробы (напр. `30s`). |
| `-check-interval` | Период фоновых задач (refresh подписок, rebuild hostlists, vpn-balance); `0`=выкл. |
| `-probe-proxy` | SOCKS5 host:port (socks-inbound sing-box) — чтобы пробы шли по пути LAN. |
| `-clash-base` | Базовый URL Clash-API (переключение селекторов). |
| `-metrics-addr` | Экспорт Prometheus `/metrics`. |
| `-audit-log` / `-fail-log` | Логи переходов / провалов проб в JSONL. |
| `-state-file` | Сохранять/восстанавливать позиции цепочек между рестартами. |
| `-zapret-*` | Каталог скриптов / active-симлинк / init-сервис nfqws. |

Полный список — `lotsmand -h`.

---

## Архитектура

Событийная, четыре компонента на шине:

```
prober ──ProductionVerdict──▶ Brain ──DesiredStateChanged──▶ Applier ──ActualStateObserved──▶ Brain
              │                  │ (сверяется с KB + intelligence)     │ (executors: zapret / vpn / direct)
              └──────────▶ KB (EWMA + D-UCB ранжирование)              └──▶ sing-box (clash-api) / nfqws
```

- **Brain** (`pkg/brain`) — машина состояний эскалации/восстановления + реконсайлер.
- **Applier** (`pkg/applier`) + **executors** (`pkg/executor`) — идемпотентное применение:
  symlink-swap nfqws + рестарт, либо флип селектора clash-api для VPN/direct.
- **KB** (`pkg/kb`) — EWMA-успех + латентность + джиттер на `(сервис, стратегия)`, D-UCB.
- **prober** (`pkg/probing`, `pkg/dataplane`) — HTTP/TCP/STUN/QUIC-пробы через SOCKS-путь.
- **eye** (`pkg/observe`, `pkg/misroute`) — пассивное чтение живой таблицы соединений:
  детект leak / dead-flow / one-way-RTC / **stall-заморозки**, что и драйвит эскалацию.

Плотная per-package карта — [`docs/CODEMAP.md`](docs/CODEMAP.md).

---

## Благодарности

Lotsman — это control-plane *поверх* проектов, которые делают сам обход. Вся заслуга — им:

- **[zapret](https://github.com/bol-van/zapret)** (bol-van) — движок DPI-десинка nfqws/tpws.
- **[zapret-discord-youtube](https://github.com/Flowseal/zapret-discord-youtube)** (Flowseal) — курированный набор стратегий (ALT-профили) и hostlists, которыми рулит Lotsman.
- **[sing-box](https://github.com/SagerNet/sing-box)** (SagerNet) — data-plane прокси/роутер.
- DPI-ресёрч сообщества: [net4people/bbs](https://github.com/net4people/bbs), ntc.party.

---

## Лицензия

GPLv3 — см. [LICENSE](LICENSE).

> ⚠️ Это личный анти-цензурный инструмент, заточенный под конкретный сетап. Выкладывается
> как референс-архитектура — рассчитывайте адаптировать конфиг (и часть допущений) под свой
> роутер, узлы и сервисы.
