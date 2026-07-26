// Shared view helpers. Kept dumb and pure so every screen renders the same
// vocabulary (a service's colour/glyph, a node's health, byte/time formatting).

export const verdictText = {
  working: 'всё работает',
  partial: 'не все сервисы доступны',
  'not-working': 'не работает',
  down: 'выключено',
}

export function verdictKind(v) {
  if (!v) return 'dim'
  if (v.state === 'working') return 'ok'
  if (v.state === 'down' || v.state === 'not-working') return 'red'
  return 'amber'
}

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
export function tileWhere(s) {
  if (s.rungClass === 'direct') return 'напрямую'
  const bits = [s.rungClass, s.strategy].filter(Boolean)
  if (s.node) bits.push(s.node)
  return bits.join(' · ')
}

// Node health → colour + label. Accepts either the ranker's names or plain ones.
export function nodeKind(state) {
  return { healthy: 'ok', alive: 'ok', degraded: 'amber', frozen: 'amber', down: 'red', dead: 'red' }[state] || 'dim'
}
export const nodeStateText = {
  healthy: 'жива',
  alive: 'жива',
  degraded: 'деградация',
  frozen: 'заморожена',
  down: 'мертва',
  dead: 'мертва',
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
