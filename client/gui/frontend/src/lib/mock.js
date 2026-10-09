// Mock /status + /events for `vite dev` in a plain browser (no Wails backend), so
// the UI can be designed and reviewed on any machine. The fleet here carries the
// alive/frozen/dead breakdown and a node list the real server does not emit YET
// (that is the deferred client-side Ranker) — the Nodes screen degrades honestly
// to "total only" when those fields are absent against the real backend.

export function mock() {
  return {
    report: {
      version: 'v7.0-14-g54c35c5 (54c35c5, 2026-08-06)',
      running: true,
      verdict: { state: 'partial', working: 6, failing: 1, broken: 1, total: 8 },
      network: { id: 'a1b2c3d4', kind: 'wifi', carrier: '', iface: 'wlan0', roaming: false },
      engines: [
        { name: 'lotsman', state: 'running' },
        { name: 'sing-box', state: 'running' },
        { name: 'nfqws', state: 'running' },
      ],
      fleet: {
        total: 50,
        servers: 6,
        pools: { vpn_url_test: 50, vpn_url_test_udp: 3, emergency_pool: 0 },
        nodes: [
          { name: '🇳🇱 Амстердам, Extra', server: '192.0.2.10', protocol: 'vless', country: 'nl', source: 'acme', pools: ['vpn_url_test'] },
          { name: '🇦🇹 Вена, Extra', server: '192.0.2.10', protocol: 'vless', country: 'at', source: 'acme', pools: ['vpn_url_test'] },
          { name: '🇨🇿 Прага, Extra', server: '192.0.2.10', protocol: 'vless', country: 'cz', source: 'acme', pools: ['vpn_url_test'] },
          { name: '🇩🇪 operator-de', server: '192.0.2.11', protocol: 'hysteria2', country: '', source: 'operator-de', pools: ['vpn_url_test', 'vpn_url_test_udp'], warm: true },
          { name: '🇳🇱 fastvpn', server: '192.0.2.12', protocol: 'hysteria2', country: '', source: 'fastvpn', pools: ['vpn_url_test', 'vpn_url_test_udp'], warm: true },
        ],
      },
      subscriptions: [
        { name: 'demo-vless', usedBytes: 8.6e9, totalBytes: 10.7e9, fractionUsed: 0.8, daysUntilExpire: 12, expired: false, expirySource: 'provider' },
        { name: 'fastsub', usedBytes: 2.1e9, totalBytes: 0, fractionUsed: -1, daysUntilExpire: 3, expired: false, expirySource: 'manual' },
      ],
      services: [
        { service: 'youtube', state: 'VPN', node: 'nl-hy2-01', rung: 0, rungClass: 'vpn', strategy: 'vpn_pool', fails: 0, broken: false },
        { service: 'discord', state: 'PREFERRED', node: '', rung: 1, rungClass: 'zapret', engine: 'nfqws', strategy: 'flowseal-alt12-discord', strategyPreset: 'ALT12', fails: 2, broken: false },
        { service: 'ai', state: 'BROKEN', node: '', rung: 3, rungClass: 'emergency', strategy: 'emergency_pool', stalledRatio: 0.91, fails: 6, broken: true },
        { service: 'web-blocked', state: 'VPN', node: 'de-vless-02', rung: 0, rungClass: 'vpn', engine: 'sing-box', strategy: 'vpn_pool', fails: 2, broken: false },
        { service: 'social', state: 'VPN', node: 'nl-hy2-01', rung: 0, rungClass: 'vpn', strategy: 'vpn_pool', fails: 0, broken: false },
        { service: 'gaming-epic', state: 'PREFERRED', node: '', rung: 0, rungClass: 'zapret', engine: 'nfqws', strategy: 'flowseal-general-fake-multisplit-664-max', strategyPreset: 'ALT12', fails: 0, broken: false },
        { service: 'dev', state: 'VPN', node: 'nl-hy2-01', rung: 0, rungClass: 'vpn', strategy: 'vpn_pool', fails: 0, broken: false },
        { service: 'ru-direct', state: 'LOCKED', node: '', rung: 0, rungClass: 'direct', strategy: 'direct', fails: 0, broken: false },
      ],
      notices: [
        {
          code: 'ipv6_escape',
          text: 'IPv6 не заходит в туннель, а у машины есть глобальный IPv6-адрес — любой сервис, чьё имя резолвится в AAAA, уходит напрямую и без защиты. Захватить его можно флагом -tun-ipv6 (нужны ноды, умеющие IPv6), либо выключить IPv6 на этом хосте.',
          services: ['ai', 'dev', 'discord', 'social', 'web-blocked'],
        },
      ],
      disabled: ['torrents', 'twitch'],
    },
    events: [
      { time: '2026-07-26T14:28:03Z', service: 'ai', from_position: 2, to_position: 3, state: 'BROKEN', strategy_class: 'emergency', reason: 'цепочка исчерпана' },
      { time: '2026-07-26T14:24:10Z', service: 'youtube', from_position: 1, to_position: 0, state: 'VPN', strategy_class: 'vpn', reason: 'предпочтительный снова здоров' },
      { time: '2026-07-26T14:19:52Z', service: 'discord', from_position: 1, to_position: 0, state: 'PREFERRED', strategy_class: 'zapret', reason: 'десинк заработал' },
      { time: '2026-07-26T14:03:20Z', service: 'web-blocked', from_position: 0, to_position: 1, state: 'VPN', strategy_class: 'vpn', reason: 'ноды заморожены' },
    ],
  }
}

// mockConfig backs the «Конфиг» editor in a plain-browser vite dev session. It
// carries the raw document text (the escape hatch) and the structured form the
// structured views edit — `config` is the document and `rules` is one entry per rule
// FILE, because that is what the service sends and what a save has to send back.
//
// A mock that carried the OLD shape would still render: the forms are driven by what
// is present, so a missing section shows as an empty editor rather than as an error.
// The demo would then look fine and be teaching a schema nothing uses.
export function mockConfig() {
  return {
    path: '/home/operator/.config/lotsman/configuration.yaml',
    rules_dir: '/home/operator/.config/lotsman/rules',
    yaml: `# Lotsman — main configuration (mock).
version: 2
target_version: "1.14.1"
proxy:
  engine: sing-box
  mode: tun
desync:
  instances:
    nfqws1-main:
      engine: nfqws1
      enabled: true
      capture:
        tcp: [80, 443]
        udp: [443]
subscriptions:
  - { name: demo-vless, url: "https://example.com/sub", format: auto, tags: [normal], enabled: true }
  - { name: fastsub, url: "https://fastsub.example/sub", format: auto, tags: [normal], enabled: true }
pools: {}
`,
    config: {
      Version: 2,
      TargetVersion: '1.14.1',
      Proxy: { Engine: 'sing-box', Mode: 'tun' },
      Desync: {
        Instances: {
          'nfqws1-main': { Engine: 'nfqws1', Enabled: true, Table: 'inet nfqueue-nfqws1-main', Capture: { TCP: ['80', '443'], UDP: ['443'] }, Connbytes: 0 },
        },
      },
      Subscriptions: [
        { Name: 'demo-vless', URL: 'https://example.com/sub', Format: 'auto', Tags: ['normal'], Enabled: true, Expires: '' },
        { Name: 'fastsub', URL: 'https://fastsub.example/sub', Format: 'auto', Tags: ['normal'], Enabled: true, Expires: '2026-09-01' },
      ],
      Pools: {
        vpn_url_test: { Type: 'url_test', Caps: ['tcp'], TagsInclude: [], TagsExclude: ['emergency'], CountriesInclude: [], CountriesExclude: ['ru'], Limit: 0, MinCoverage: 0, Warmup: false, Interval: '1m0s', IdleTimeout: '30m0s' },
        vpn_url_test_udp: { Type: 'url_test', Caps: ['udp_native'], TagsInclude: [], TagsExclude: [], CountriesInclude: [], CountriesExclude: ['ru'], Limit: 0, MinCoverage: 0, Warmup: true, Interval: '1m0s', IdleTimeout: '' },
      },
      Hostlists: [
        { Name: 'ru-blocked', Out: '/var/lib/lotsman/ru-blocked.txt', Sources: ['https://example.com/blocked.txt'], Exclude: [], Domains: [], MinKeepRatio: 0.8 },
        { Name: 'мои', Out: '/var/lib/lotsman/mine.txt', Sources: [], Exclude: [], Domains: ['bank.example', 'work.example'], MinKeepRatio: 0 },
      ],
      Strategies: [
        { ID: 'flowseal-fake-multisplit', Class: 'zapret', NFQWSArgs: ['--dpi-desync=fake,multisplit', '--dpi-desync-fooling=md5sig'], BlockTypes: ['tcp_reset'], Notes: 'победитель на этом провайдере' },
      ],
      DNS: {
        Servers: [
          { Name: 'remote', URL: '', Provider: 'cloudflare', Method: 'https', Type: '', Address: '', Port: 0, Path: '', ServerName: '', Detour: 'vpn' },
          { Name: 'lan', URL: '', Provider: '', Method: '', Type: 'local', Address: '', Port: 0, Path: '', ServerName: '', Detour: '' },
          { Name: 'desync-pub', URL: '', Provider: 'quad9', Method: 'tls', Type: '', Address: '', Port: 0, Path: '', ServerName: '', Detour: 'direct' },
        ],
        Direct: 'lan',
        Zapret: 'desync-pub',
        Final: 'remote',
        Strategy: 'prefer_ipv4',
        FakeIP: false,
        Failover: ['cloudflare', 'quad9', 'mullvad'],
        DirectFailover: ['cloudflare', 'google'],
      },
      Multiplex: { Enabled: true, Protocol: 'h2mux', MaxConnections: 1, MinStreams: 4, Padding: true, BrutalUp: 0, BrutalDown: 0 },
      UTLSFingerprint: 'chrome',
      SubViaPool: '',
    },
    rules: [
      {
        file: 'youtube.yaml',
        rule: {
          ID: 'youtube',
          Name: 'YouTube',
          Enabled: true,
          Priority: 0,
          Match: { Domains: ['youtube.com', 'googlevideo.com', 'ytimg.com'], DomainLists: ['ru-blocked'], IPLists: [], CIDRs: [] },
          Probe: { Type: 'http', Target: 'https://www.youtube.com/generate_204' },
          Volume: { Targets: ['https://www.youtube.com/'], MinBytes: 0 },
          Behavior: { Sticky: true, Profile: 'streaming' },
          Chain: [
            { Tier: 'preferred', Action: { Desync: { Instance: 'nfqws1-main', Strategy: 'flowseal-fake-multisplit' } } },
            { Tier: 'preferred', Action: { Desync: { Instance: 'nfqws1-main', Strategy: 'auto' } } },
            { Tier: 'fallback', Action: { VPN: { Pool: 'vpn_url_test_udp' } } },
            { Tier: 'emergency', Action: { VPN: { Pool: 'emergency_pool' } } },
          ],
        },
      },
      {
        file: 'discord.yaml',
        rule: {
          ID: 'discord',
          Name: 'Discord',
          Enabled: true,
          Priority: 0,
          Match: { Domains: ['discord.com', 'discord.gg', 'discordapp.com'], DomainLists: [], IPLists: [], CIDRs: ['66.22.192.0/18'] },
          Probe: { Type: 'http', Target: 'https://discord.com/api/v9/gateway' },
          Behavior: { EscalateAfter: 3 },
          Chain: [
            { Tier: 'preferred', Action: { Desync: { Instance: 'nfqws1-main', Strategy: 'auto' } } },
            { Tier: 'fallback', Action: { VPN: { Pool: 'vpn_url_test_udp' } } },
            { Tier: 'emergency', Action: { VPN: { Pool: 'emergency_pool' } } },
          ],
        },
      },
    ],
  }
}

// First-run, for `vite dev` in a plain browser. The window asks SetupState before it
// asks /status, so without this a dev session would try first-run on every reload and
// never reach the dashboard it exists to design.
//
// It reports a CONFIGURED machine, which is the interesting case to design against:
// that is what every existing screen renders. The unconfigured case is reachable by
// setting `?fresh=1` in the URL, because "what does a new user see" is exactly the
// screen nobody looks at until it ships and then everybody does.
export function mockSetup() {
  const fresh =
    typeof window !== 'undefined' && /[?&]fresh=1/.test(window.location.search)
  return {
    configured: !fresh,
    source_path: '/home/user/.config/lotsman',
    service_reachable: !fresh,
  }
}
