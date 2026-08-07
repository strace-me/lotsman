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
        { service: 'discord', state: 'PREFERRED', node: '', rung: 1, rungClass: 'zapret', engine: 'nfqws', strategy: 'flowseal-alt12-discord', fails: 2, broken: false },
        { service: 'ai', state: 'BROKEN', node: '', rung: 3, rungClass: 'emergency', strategy: 'emergency_pool', stalledRatio: 0.91, fails: 6, broken: true },
        { service: 'web-blocked', state: 'VPN', node: 'de-vless-02', rung: 0, rungClass: 'vpn', engine: 'sing-box', strategy: 'vpn_pool', fails: 2, broken: false },
        { service: 'social', state: 'VPN', node: 'nl-hy2-01', rung: 0, rungClass: 'vpn', strategy: 'vpn_pool', fails: 0, broken: false },
        { service: 'gaming-epic', state: 'PREFERRED', node: '', rung: 0, rungClass: 'zapret', strategy: 'alt-zapret', fails: 0, broken: false },
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
// carries both the raw YAML (escape hatch) and the structured `doc` (PascalCase
// keys, mirroring config.Document's default JSON) the structured forms edit.
export function mockConfig() {
  return {
    path: '/home/operator/.config/lotsman/config.yaml',
    yaml: `# Lotsman client config (mock).
subscriptions:
  - { name: demo-vless, url: "https://example.com/sub", format: auto, tags: [normal], enabled: true }
  - { name: fastsub, url: "https://fastsub.example/sub", format: auto, tags: [normal], enabled: true }
services:
  - name: youtube
    category: streaming
    probe_target: https://www.youtube.com/generate_204
    domains: [youtube.com, googlevideo.com, ytimg.com]
  - name: discord
    category: messaging
    probe_target: https://discord.com/api/v9/gateway
    domains: [discord.com, discord.gg, discordapp.com]
`,
    doc: {
      Subscriptions: [
        { Name: 'demo-vless', URL: 'https://example.com/sub', Format: 'auto', Tags: ['normal'], Enabled: true, Expires: '' },
        { Name: 'fastsub', URL: 'https://fastsub.example/sub', Format: 'auto', Tags: ['normal'], Enabled: true, Expires: '2026-09-01' },
      ],
      Services: [
        { Name: 'youtube', Category: 'streaming', ProbeTarget: 'https://www.youtube.com/generate_204', Domains: ['youtube.com', 'googlevideo.com', 'ytimg.com'], DomainLists: ['ru-blocked'] },
        { Name: 'discord', Category: 'messaging', ProbeTarget: 'https://discord.com/api/v9/gateway', Domains: ['discord.com', 'discord.gg', 'discordapp.com'], DomainLists: [] },
      ],
      Pools: {
        vpn_url_test: { Type: 'url_test', Filter: { Caps: ['tcp'], TagsInclude: [], TagsExclude: ['emergency'], CountriesInclude: [], CountriesExclude: ['ru'] }, Warmup: false, Interval: '1m', IdleTimeout: '30m' },
        vpn_url_test_udp: { Type: 'url_test', Filter: { Caps: ['udp_native'], TagsInclude: [], TagsExclude: [], CountriesInclude: [], CountriesExclude: ['ru'] }, Warmup: true, Interval: '1m', IdleTimeout: '' },
      },
      Hostlists: [
        { Name: 'ru-blocked', Out: '/var/lib/lotsman/ru-blocked.txt', Sources: ['https://example.com/blocked.txt'], Exclude: [], Domains: [], MinKeepRatio: 0.8 },
        { Name: 'мои', Out: '/var/lib/lotsman/mine.txt', Sources: [], Exclude: [], Domains: ['bank.example', 'work.example'], MinKeepRatio: 0 },
      ],
      DNS: {
        Servers: [
          { Name: 'remote', Provider: 'cloudflare', Method: 'https', Type: '', Address: '', ServerName: '', Path: '', Detour: 'vpn' },
          { Name: 'lan', Provider: '', Method: '', Type: 'local', Address: '', ServerName: '', Path: '', Detour: '' },
        ],
        Direct: 'lan',
        Final: 'remote',
        Strategy: 'prefer_ipv4',
        FakeIP: false,
        Failover: ['cloudflare', 'quad9', 'mullvad'],
      },
      UTLSFingerprint: 'chrome',
      SingboxVersion: '1.13.14',
      Multiplex: { Enabled: true, Protocol: 'h2mux', MaxConnections: 1, MinStreams: 4, Padding: true, BrutalUp: 0, BrutalDown: 0 },
      FakeIP: null,
      Strategies: [
        { ID: 'flowseal-fake-multisplit', Class: 'zapret', NFQWSArgs: ['--dpi-desync=fake,multisplit', '--dpi-desync-fooling=md5sig'], BlockTypes: ['rst'], Notes: 'победитель на этом провайдере' },
      ],
    },
  }
}
