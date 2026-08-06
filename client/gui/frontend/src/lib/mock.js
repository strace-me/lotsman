// Mock /status + /events for `vite dev` in a plain browser (no Wails backend), so
// the UI can be designed and reviewed on any machine. The fleet here carries the
// alive/frozen/dead breakdown and a node list the real server does not emit YET
// (that is the deferred client-side Ranker) — the Nodes screen degrades honestly
// to "total only" when those fields are absent against the real backend.

export function mock() {
  return {
    report: {
      running: true,
      verdict: { state: 'partial', working: 6, failing: 1, broken: 1, total: 8 },
      network: { id: 'a1b2c3d4', kind: 'wifi', carrier: '', iface: 'wlan0', roaming: false },
      engines: [
        { name: 'lotsman', state: 'running' },
        { name: 'sing-box', state: 'running' },
        { name: 'nfqws', state: 'running' },
      ],
      fleet: {
        total: 150,
        alive: 132,
        frozen: 12,
        dead: 6,
        nodes: [
          { node: 'nl-hy2-01', sub: 'demo-vless', state: 'healthy', rttMs: 34 },
          { node: 'nl-vless-04', sub: 'demo-vless', state: 'healthy', rttMs: 41 },
          { node: 'de-vless-02', sub: 'demo-vless', state: 'degraded', rttMs: 88 },
          { node: 'fr-hy2-03', sub: 'demo-vless', state: 'frozen', rttMs: 0 },
          { node: 'de-hy2-06', sub: 'fastsub', state: 'healthy', rttMs: 52 },
          { node: 'us-hy2-05', sub: 'fastsub', state: 'dead', rttMs: 0 },
          { node: 'nl-hy2-07', sub: 'fastsub', state: 'healthy', rttMs: 46 },
        ],
      },
      subscriptions: [
        { name: 'demo-vless', usedBytes: 8.6e9, totalBytes: 10.7e9, fractionUsed: 0.8, daysUntilExpire: 12, expired: false },
        { name: 'fastsub', usedBytes: 2.1e9, totalBytes: 0, fractionUsed: -1, daysUntilExpire: 3, expired: false },
      ],
      services: [
        { service: 'youtube', state: 'VPN', node: 'nl-hy2-01', rung: 0, rungClass: 'vpn', strategy: 'vpn_pool', fails: 0, broken: false },
        { service: 'discord', state: 'PREFERRED', node: '', rung: 0, rungClass: 'zapret', strategy: 'flowseal-syndata', fails: 0, broken: false },
        { service: 'ai', state: 'BROKEN', node: '', rung: 3, rungClass: 'emergency', strategy: 'emergency_pool', stalledRatio: 0.91, fails: 6, broken: true },
        { service: 'web-blocked', state: 'VPN', node: 'de-vless-02', rung: 0, rungClass: 'vpn', strategy: 'vpn_pool', fails: 2, broken: false },
        { service: 'social', state: 'VPN', node: 'nl-hy2-01', rung: 0, rungClass: 'vpn', strategy: 'vpn_pool', fails: 0, broken: false },
        { service: 'gaming-epic', state: 'PREFERRED', node: '', rung: 0, rungClass: 'zapret', strategy: 'alt-zapret', fails: 0, broken: false },
        { service: 'dev', state: 'VPN', node: 'nl-hy2-01', rung: 0, rungClass: 'vpn', strategy: 'vpn_pool', fails: 0, broken: false },
        { service: 'ru-direct', state: 'LOCKED', node: '', rung: 0, rungClass: 'direct', strategy: 'direct', fails: 0, broken: false },
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
        { Name: 'demo-vless', URL: 'https://example.com/sub', Format: 'auto', Tags: ['normal'], Enabled: true },
        { Name: 'fastsub', URL: 'https://fastsub.example/sub', Format: 'auto', Tags: ['normal'], Enabled: true },
      ],
      Services: [
        { Name: 'youtube', Category: 'streaming', ProbeTarget: 'https://www.youtube.com/generate_204', Domains: ['youtube.com', 'googlevideo.com', 'ytimg.com'], DomainLists: ['ru-blocked'] },
        { Name: 'discord', Category: 'messaging', ProbeTarget: 'https://discord.com/api/v9/gateway', Domains: ['discord.com', 'discord.gg', 'discordapp.com'], DomainLists: [] },
      ],
      Hostlists: [
        { Name: 'ru-blocked', Out: '/var/lib/lotsman/ru-blocked.txt', Sources: ['https://example.com/blocked.txt'], Exclude: [], MinKeepRatio: 0.8 },
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
