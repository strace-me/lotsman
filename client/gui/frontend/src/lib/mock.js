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
    },
    events: [
      { time: '2026-07-26T14:28:03Z', service: 'ai', from_position: 2, to_position: 3, state: 'BROKEN', strategy_class: 'emergency', reason: 'цепочка исчерпана' },
      { time: '2026-07-26T14:24:10Z', service: 'youtube', from_position: 1, to_position: 0, state: 'VPN', strategy_class: 'vpn', reason: 'предпочтительный снова здоров' },
      { time: '2026-07-26T14:19:52Z', service: 'discord', from_position: 1, to_position: 0, state: 'PREFERRED', strategy_class: 'zapret', reason: 'десинк заработал' },
      { time: '2026-07-26T14:03:20Z', service: 'web-blocked', from_position: 0, to_position: 1, state: 'VPN', strategy_class: 'vpn', reason: 'ноды заморожены' },
    ],
  }
}
