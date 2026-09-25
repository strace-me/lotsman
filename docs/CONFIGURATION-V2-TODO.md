# Configuration v2 — рабочий TODO и критерии готовности

> Статус: design / implementation not started for v2.
> Обновлено: 2026-09-25.
> Связанный design spec: `ObsidianDB/Projects/Lotsman/docs/DESIGN-configuration-v2.md`.
> Этот файл — не история обсуждения, а исполняемый план: каждый пункт имеет результат и проверку.

## Статус сейчас

### Уже сделано в текущем рабочем дереве

- [x] Sandbox TCP probe переведён с `SO_BINDTODEVICE` на `LocalAddr` физического интерфейса.
- [x] Сохранён sandbox fwmark `0x4554` и bypass routing rule.
- [x] Sandbox probe использует `github.com/metacubex/utls v1.8.4`, тот же форк, что уже используется mobile-модулем.
- [x] HTTP/3 sandbox socket использует тот же physical source-address подход.
- [x] Root lanesim подтвердил: baseline и ALT12 завершают TLS с `h2`.
- [x] Добавлен отдельный тест выбора локального адреса.
- [x] Детальный handoff лежит в `/tmp/opencode/HANDOFF.md`.

### Не сделано

- [ ] v2 configuration parser и schema.
- [ ] `/etc/lotsman/rules/` как источник правил.
- [ ] strategy bundle store.
- [ ] nfqws2 engine execution.
- [ ] multi-instance desync executor.
- [ ] engine-aware KB.
- [ ] strategy CRUD API.
- [ ] GUI/LuCI strategy editor.
- [ ] краткий runtime-лог canary.
- [ ] ручной endpoint проверки конкретной стратегии.
- [ ] миграция `client.yaml` в `configuration.yaml`.

## Договорённые архитектурные решения

1. Канонический конфиг называется `/etc/lotsman/configuration.yaml`.
2. Desktop, `lotsmand` и будущий LuCI читают одну модель конфигурации.
3. `sing-box` — proxy core, `nfqws1`/`nfqws2` — desync engines. Они не смешиваются в один список.
4. Пользовательские стратегии хранятся в `/etc/lotsman/desync/strategies/<engine>/<id>/`.
5. Стратегия — bundle: metadata, engine-specific `command.args`, hostlists, payloads, Lua.
6. Один bundle принадлежит ровно одному engine.
7. Аргументы nfqws1 и nfqws2 не смешиваются и не валидируются общим набором флагов.
8. Правила можно хранить отдельными файлами в `/etc/lotsman/rules/`.
9. Доменные и IP данные имеют именованные списки и внешние providers.
10. `ipset` — compiled representation, а не пользовательское поле конфигурации.
11. `behavior.profile` описывает `general`, `streaming`, `voice`, `gaming` и раскрывается в явные defaults.
12. Sing-box proxy settings не выгружаются вручную в конфиг: Lotsman генерирует runtime-конфиг из намерений.
13. Strategy args и payloads не живут в `configuration.yaml`.
14. Новые GUI-записи пишутся только в canonical store; старые форматы — read-only adapters на переходный период.
15. Autocomplete строится из общей JSON Schema и metadata API, а не из вручную скопированных Svelte-массивов.

## Этап 0. Зафиксировать дизайн

### 0.1 Утвердить schema v2

- [ ] Определить минимальный `configuration.yaml` без runtime-флагов.
- [ ] Определить, где остаются deployment flags и что является override.
- [ ] Решить, входит ли `runtime:` в YAML или остаётся отдельным deployment-конфигом.
- [ ] Определить имена `proxy`, `desync`, `rules`, `lists`, `data_sources`, `subscriptions`, `pools`, `dns`.
- [ ] Определить, остаётся ли `category` или заменяется на profile/behavior defaults.
- [ ] Записать примеры `reddit.yaml`, `steam.yaml`, `youtube.yaml`.
- [ ] Проверить, что примеры проходят strict parse и semantic validation.

**Готово когда:** один пример v2 можно положить в `examples/` и загрузить без специальных флагов.

### 0.2 Утвердить внешние файлы

- [ ] Решить, `rule_sources.directory` или `rules.directory` — имя в схеме.
- [ ] Определить сортировку и детерминированность загрузки файлов.
- [ ] Определить формат файла правила: YAML-only или YAML + text assets.
- [ ] Решить, где хранятся user-authored list files: `/etc/lotsman/lists/` или рядом с bundle.
- [ ] Определить поведение при битом/пустом rule-файле: refuse startup или last-known-good.
- [ ] Запретить path traversal и absolute path вне разрешённых каталогов.
- [ ] Добавить тесты duplicate id, unreadable file, empty match, stale list reference.

**Готово когда:** два независимых rule-файла собираются в один config, а один битый файл не тихо превращается в «правило без трафика».

## Этап 1. Shared store и schema

### 1.1 `pkg/strategystore`

- [ ] Вынести текущий `.args` parser из `pkg/config`.
- [ ] Не создавать третий parser: существующий `loadPreset` должен быть общей primitive.
- [ ] Сделать parse/write bundle manifest.
- [ ] Сделать parse/write `command.args` с comments и blank lines.
- [ ] Сделать atomic write: temp file, fsync, rename.
- [ ] Сделать content revision/hash.
- [ ] Сделать last-known-good snapshot при reload с ошибкой.
- [ ] Ограничить id регулярным выражением и запретить traversal.
- [ ] Проверять существование assets, но не загружать/выполнять их.

**Файлы:** новый `pkg/strategystore`, тесты рядом; `pkg/config` перестаёт быть единственным владельцем формата.

### 1.2 Bundle format

- [ ] Утвердить `strategy.yaml` fields: `id`, `name`, `engine`, `args`, `assets`, `revision`, `notes`.
- [ ] Утвердить layout `hostlists/`, `payloads/`, `lua/`.
- [ ] Утвердить, что `command.args` — opaque argv, а не shell command.
- [ ] Добавить nfqws1 и nfqws2 fixtures.
- [ ] Проверить, что nfqws2 `--blob=name:@file` и `--lua-desync=@file` не проходят через nfqws1 validator.
- [ ] Проверить, что absolute asset paths допускаются только внутри bundle или явно разрешённого каталога.

**Готово когда:** один и тот же bundle loader читает nfqws1 и nfqws2, но разные engine adapters принимают только свои capabilities.

## Этап 2. Data sources и lists

### 2.1 Provider abstraction

- [ ] Описать `data_sources` для `runetfreedom/russia-v2ray-rules-dat`.
- [ ] Описать `russia-blocked-geosite` как альтернативный provider.
- [ ] Поддержать geosite/geoip `.dat` sources и refresh interval.
- [ ] Проверять checksum/sha256, если источник его публикует.
- [ ] Не различать `category: reddit` и `field: reddit` в пользовательском API без необходимости.
- [ ] Сообщать, если provider не содержит запрошенную категорию.

### 2.2 Lists

- [ ] Реализовать domain lists из provider categories.
- [ ] Реализовать IP/CIDR lists из manual files и provider categories.
- [ ] `geosite:reddit` и `geoip:reddit` проверять независимо.
- [ ] Не подменять отсутствующий `geoip:reddit` списком `ru-blocked`.
- [ ] Поддержать manual `reddit.com` рядом с именованным списком.
- [ ] Сохранять source metadata для диагностики и UI.
- [ ] Добавить update freshness/status.

### 2.3 Компиляция под движки

- [ ] Один source → compiled representation для sing-box.
- [ ] Один source → compiled representation для nfqws1.
- [ ] Один source → compiled representation для nfqws2.
- [ ] Сохранять generated files в runtime dir, не перезаписывая пользовательские `/etc` assets.
- [ ] Проверять, что domain-only list не обещает desync для IP-only трафика.
- [ ] Добавить coherence warning для `GapIPZapret` и `GapRuleSetZapret`.

**Готово когда:** обновление runetfreedom не требует редактирования rule-файлов и не приводит к тихой потере доменов.

## Этап 3. Behavior и chain

### 3.1 Behavior profiles

- [ ] `general` → `general_tls`, HTTP probe.
- [ ] `streaming` → `youtube`, `quic`, `general_tls`, volume probe.
- [ ] `voice` → `discord_tcp`, `general_tls`, STUN/UDP probe.
- [ ] `gaming` → `general_tls`, `games`, explicit transport/ports.
- [ ] Не создавать blanket UDP filter для gaming.
- [ ] Требовать explicit scope для unscopable game profile.
- [ ] Разделить `probe`, `transport`, `routing` и `node` defaults.
- [ ] Сохранить `sticky`, `spread_clients`, `tls_fragment` в явном behavior или rule policy.

### 3.2 Chain actions

- [ ] Определить v2 actions: `desync`, `vpn`, `direct`, `bypass`.
- [ ] Убрать дублирование `state` и `class` из пользовательского API.
- [ ] Сохранить runtime state в status, но не в конфиге.
- [ ] Определить `preferred`, `fallback`, `emergency` как стадии или просто позиции chain.
- [ ] Проверить, что `strategy: auto` и явный `strategy: alt12` различимы.
- [ ] Проверить, что удалённая/неизвестная strategy даёт ошибку, а не silently empty.

**Готово когда:** chain можно прочитать без знания внутренних state names, а runtime status показывает фактическое положение.

## Этап 4. Engine/pool/instance

### 4.1 Data model

- [ ] `EngineDescriptor` с capabilities и validator.
- [ ] `StrategyKey {engine, pool, id}`.
- [ ] `Instance {name, engine, binary, qnum, capture, pool}`.
- [ ] `nfqws1` и `nfqws2` descriptors.
- [ ] `byedpi` либо поддержать, либо убрать из GUI, чтобы не предлагать неработающий class.
- [ ] `winws` отражать отдельно от nfqws на desktop.

### 4.2 Multi-instance execution

- [ ] Заменить один глобальный `c.zap` на map instances.
- [ ] Привязать service к instance через chain action.
- [ ] Разделить qnum/capture/connbytes defaults.
- [ ] Не запускать пустые instances.
- [ ] Останавливать один instance без остановки других.
- [ ] Статус должен показывать engine/instance/pool/strategy.
- [ ] Sandbox должен выбирать instance явно.

### 4.3 nfqws2

- [ ] `--lua-desync` payload validation.
- [ ] `--blob=name:@file` parsing.
- [ ] Стандартные `fake_default_*` blobs отличать от custom files.
- [ ] Payload path resolution внутри bundle.
- [ ] Никакого `strings.Join` в shell renderer для внешних argv.
- [ ] Engine-specific rollback при неудачном старте.
- [ ] Никакого silent fallback на nfqws1.

**Готово когда:** nfqws2 bundle запускается тем же executor-путём, но получает только свои args и assets.

## Этап 5. KB и migration

### 5.1 Namespacing

- [ ] Новые KB keys: `service|engine|pool|strategyID`.
- [ ] Старые keys читать как `nfqws1|legacy-default|strategyID`.
- [ ] Сохранить старую KB-историю при первой загрузке.
- [ ] Раздельно считать результаты nfqws1 и nfqws2 с одинаковым id.
- [ ] Перенести per-network KB path без изменения имени файла.

### 5.2 Config migration

- [ ] Определить mapping `services` → `rules/*.yaml`.
- [ ] Сохранить `id`, category defaults, chain positions и probes.
- [ ] Перенести `domains` → `match.domains`.
- [ ] Перенести `domain_lists` → `match.domain_lists`.
- [ ] Перенести `ips`/`ips_file` → `match.cidrs`/`match.ip_lists`.
- [ ] Перенести `rule_sets` в provider/list representation.
- [ ] Перенести `profile` → `behavior.profile`.
- [ ] Перенести `sticky`, `spread_clients`, `static` без потери семантики.
- [ ] Старый `zapret.preset` читать как legacy adapter до миграции.
- [ ] Сделать dry-run migration report: что перенесено, что потеряно, что не распознано.

**Готово когда:** текущий рабочий конфиг можно скопировать в v2, применить и получить тот же routing/desync без ручного переписывания правил.

## Этап 6. API, GUI, LuCI

### 6.1 Schema/metadata API

- [ ] Экспортировать config types для schema generation.
- [ ] `GET /schema`.
- [ ] `GET /metadata` или расширенный `/strategies` с engine capabilities.
- [ ] Metadata содержит enums, pools, lists, strategies, providers и behavior profiles.
- [ ] Убрать ручные enum-копии из Svelte.
- [ ] Добавить frontend TypeScript types из schema.

### 6.2 Strategy API

- [ ] `GET /strategies`.
- [ ] `POST /strategies/{engine}/{id}/validate`.
- [ ] `PUT /strategies/{engine}/{id}`.
- [ ] `DELETE /strategies/{engine}/{id}`.
- [ ] Revision conflict detection.
- [ ] Atomic writes.
- [ ] Reload без немедленного restart.
- [ ] Ошибки показываются как engine-specific validation messages.

### 6.3 Rule API/GUI

- [ ] CRUD для rule-файлов.
- [ ] Rule form показывает match, behavior, chain и probe.
- [ ] Engine selector скрыт, если для сервиса только один engine.
- [ ] Strategy picker показывает только стратегии выбранного engine/pool.
- [ ] List picker показывает source, freshness и errors.
- [ ] Raw YAML editor остаётся escape hatch.

### 6.4 LuCI parity

- [ ] LuCI читает тот же `configuration.yaml`.
- [ ] LuCI пишет через тот же control API или privileged helper.
- [ ] Не дублировать YAML schema в LuCI.
- [ ] Desktop и router показывают одинаковые engine/instance names.

## Этап 7. Canary и observability

- [ ] Runtime sandbox record: `service`, `strategy`, `engine`, `instance`, `purpose`, `profile`, verdict.
- [ ] Убрать полный argv из рутинного runtime-лога.
- [ ] Crash log оставить с полным argv для диагностики.
- [ ] Добавить понятные фазы: candidate started, measured, passed, rejected, control.
- [ ] `POST /service/{name}/recheck?strategy={id}` запускает ровно выбранный bundle.
- [ ] Endpoint возвращает accepted/rejected/unknown engine/strategy.
- [ ] Ручная проверка не меняет production traffic до прохождения.
- [ ] Проверять, что rule всё ещё на desync rung до и после измерения.

## Этап 8. Тесты и приёмка

### Unit

- [ ] v2 schema decode/validate.
- [ ] rule-file loader.
- [ ] strategy bundle parser/writer.
- [ ] provider category resolution.
- [ ] list compilation для каждого engine.
- [ ] behavior profile expansion.
- [ ] chain action resolution.
- [ ] engine capability validation.
- [ ] KB namespace migration.

### Integration

- [ ] runetfreedom provider → sing-box rules.
- [ ] runetfreedom provider → nfqws1 hostlist.
- [ ] nfqws2 bundle → validation and argv launch.
- [ ] multi-instance capture/qnum validation.
- [ ] API CRUD с unprivileged UI.
- [ ] migration old config → v2.

### Live

- [ ] Existing desktop daily driver.
- [ ] Existing R5S config.
- [ ] YouTube sandbox candidate ALT12.
- [ ] Manually forced Reddit/Steam rule.
- [ ] Engine rollback leaves previous instance alive.
- [ ] Provider refresh does not drop a good list.

## Открытые решения перед началом кода

1. `runtime:` в YAML или deployment-only flags.
2. Точное имя списка IP: `ip_lists` достаточно или нужен `ipset` для пользователя.
3. Default expansion `behavior: gaming`.
4. Формат отдельного rule-файла и максимальный размер списка.
5. Поведение отсутствующей provider category.
6. Comment preservation через `yaml.Node`.
7. Нужны ли `excluded` списки в v2 и где их место.
8. Direct/bypass actions в первой версии.
9. Обязательный минимум v2 для non-root proxy mode.
10. Порядок удаления legacy `strategies:` и `zapret.preset`.

## Правило для будущих сессий

- Сначала читать этот файл и `DESIGN-configuration-v2.md`.
- Не начинать с nfqws2 или GUI, пока не закрыты schema и store.
- Не создавать новый формат рядом со старым без адаптера и плана удаления.
- Каждый этап заканчивается тестом и live-примером, а не только компиляцией.
- Любое решение, меняющее user-facing YAML, сначала фиксируется в design spec, потом в этом TODO.
