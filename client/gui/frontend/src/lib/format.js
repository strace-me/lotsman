// Shared view helpers. Kept dumb and pure so every screen renders the same
// vocabulary (a service's colour/glyph, a node's health, byte/time formatting).

// A service is red when its chain is exhausted, amber while it is still failing
// at its current rung (подбор), dim when unmanaged (direct), else ok. This mirrors
// the server's honest verdict — a failing service is never painted green.
export function tileKind(s) {
  if (s.broken) return 'red'
  if (s.fails > 0) return 'amber'
  if (s.rungClass === 'direct') return 'dim'
  return 'ok'
}
export function tileGlyph(s) {
  if (s.broken) return '✗'
  if (s.fails > 0) return '⟳'
  if (s.rungClass === 'direct') return '·'
  return '✓'
}
// Which ENGINE is carrying this rule, then what it is running, then where. The
// engine comes first because it decides where you look when a service misbehaves:
// nfqws means desync recipes, sing-box means nodes and pools. The rung class alone
// never said it.
export function tileWhere(s) {
  if (s.rungClass === 'direct') return [s.engine, 'напрямую'].filter(Boolean).join(' · ')
  // The recipe id plus the upstream's own name for it, because the operator
  // thinks in "ALT12" and the app only ever said flowseal-general-fake-…-664-max.
  const strat = s.strategy && s.strategyPreset ? `${s.strategy} (${s.strategyPreset})` : s.strategy
  const bits = [s.engine || s.rungClass, strat].filter(Boolean)
  if (s.node && s.node !== s.strategy) bits.push(s.node)
  return bits.join(' · ')
}

// What the brain ASKED for, shown only when the data plane is running something
// else. That divergence is silent today — the brain can name a strategy the local
// engine cannot render, and the executor falls back without saying so.
export function tileRequested(s) {
  return s.requested ? 'запрошено: ' + s.requested : ''
}

export function fmtBytes(n) {
  if (!n) return '—'
  const gb = n / 1e9
  return gb >= 1 ? gb.toFixed(1) + ' ГБ' : (n / 1e6).toFixed(0) + ' МБ'
}
export function fmtTime(t) {
  const d = new Date(t)
  return isNaN(d) ? '' : d.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' })
}

// "проверено N назад". The dashboard shows it on every rule because the recheck
// button posts onto a channel that DROPS the request when one is already queued —
// "I clicked and nothing changed" is a real outcome, and a timestamp that visibly
// resets is the only feedback that comes from the probe rather than from the click.
export function fmtAgo(ms, now) {
  if (!ms) return ''
  const s = Math.max(0, Math.round((now - ms) / 1000))
  if (s < 3) return 'только что'
  if (s < 60) return s + ' с назад'
  const m = Math.round(s / 60)
  return m < 60 ? m + ' мин назад' : Math.round(m / 60) + ' ч назад'
}
