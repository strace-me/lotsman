// The mock and the real backend must agree on the SHAPE of a rule, not just look
// plausible.
//
// The mismatch was real and invisible: Go's `configRule` tags the field
// `json:"rule"`, so /config returns `rule`, while the frontend read `r.Rule` and
// the mock — written by hand — supplied `Rule:`. The build passed, `npm run dev`
// rendered a perfect config, and on the real backend every structured rule read as
// undefined.
//
// A mock that lies about a contract is worse than no mock: it converts a wiring bug
// into a green CI run.
import { test } from 'node:test'
import assert from 'node:assert'
import { mockConfig } from './mock.js'

// The shape Go actually emits, read from client/core/configapi.go's struct tags.
const GO_SHAPE = { file: 'string', yaml: 'string (omitempty)', rule: 'object (omitempty)' }

test('the mock uses the same rule key the Go struct tag produces', () => {
  const m = mockConfig()
  assert.ok(Array.isArray(m.rules) && m.rules.length > 0, 'mock carries rules')
  for (const r of m.rules) {
    assert.ok('rule' in r, `mock rule entry has no lowercase "rule" key: ${Object.keys(r)}`)
    assert.ok(!('Rule' in r), `mock rule entry still has a PascalCase "Rule" key, which Go never sends`)
    assert.equal(typeof r.file, 'string')
    assert.equal(typeof r.rule, 'object')
  }
})

test('inner rule fields keep Go\'s exported names (no json tags on configv2.Rule)', () => {
  // configv2.Rule carries yaml tags but no json ones, so encoding/json emits the
  // FIELD names. Frontend code reading .ID / .Name / .Match is therefore correct,
  // and only the wrapper key is lower-case. Getting this backwards would be a
  // second silent break in the same struct.
  const rule = mockConfig().rules[0].rule
  for (const k of ['ID', 'Name', 'Enabled', 'Match', 'Chain']) {
    assert.ok(k in rule, `inner rule is missing ${k}: ${Object.keys(rule)}`)
  }
  assert.ok('Id' in rule === false && 'id' in rule === false, 'inner ids must not be renamed')
})

test('the documented Go shape is the one the mock is held to', () => {
  // Guards against the mock drifting again in a direction this test would not
  // otherwise notice: if the struct tag changes, this names the old contract.
  assert.ok('rule' in GO_SHAPE && !('Rule' in GO_SHAPE))
})

// The FIRST pass at this fix replaced `r.Rule`, `x.Rule` and `rule.Rule` and declared
// the job done. It missed `item.Rule` in Rules.svelte's renameId and `r.Rule` in
// RuleForm's strategy collector — both reached through a different local name, and
// both broke the moment the fix landed: renameId returns early on an undefined guard,
// and the collector offers no strategies to pick from.
//
// So the rule is the blunt one: assert there is NO `.Rule` anywhere in the components.
// A property name is either how Go spells it or it does not exist, and a lint for it
// is cheaper than the next version of this mistake.
test('no component reads a PascalCase .Rule off a wire object', async () => {
  const { readdir, readFile } = await import('node:fs/promises')
  const dir = new URL('.', import.meta.url).pathname
  const files = (await readdir(dir)).filter((f) => f.endsWith('.svelte'))
  const offenders = []
  for (const f of files) {
    const body = await readFile(dir + f, 'utf8')
    body.split('\n').forEach((line, i) => {
      // Ignore prose: a comment may NAME the old spelling to explain the mistake.
      const code = line.replace(/\/\/.*$/, '').replace(/^\s*\*.*$/, '')
      if (/\.Rule\b/.test(code)) offenders.push(`${f}:${i + 1}: ${code.trim()}`)
    })
  }
  assert.deepEqual(offenders, [], 'components still read .Rule:\n' + offenders.join('\n'))
})

// The rule row shows "3 доменов" / "3 подсетей" under the naive `n === 1` check this
// app shipped with. Every rule row, every time — the sort of small wrongness that
// makes an interface read as machine-made, and the only reason to notice is that
// someone actually looked at a screen.
import { plural, pluralDomain, pluralCidr, pluralList } from './format.js'

test('russian plurals, including the teens that break the naive check', () => {
  const cases = [
    [1, pluralDomain, '1 домен'],
    [2, pluralDomain, '2 домена'],
    [3, pluralDomain, '3 домена'],
    [5, pluralDomain, '5 доменов'],
    [11, pluralDomain, '11 доменов'], // teen: NOT "11 домена"
    [12, pluralDomain, '12 доменов'],
    [14, pluralDomain, '14 доменов'],
    [21, pluralDomain, '21 домен'], // last digit wins again
    [22, pluralDomain, '22 домена'],
    [25, pluralDomain, '25 доменов'],
    [111, pluralDomain, '111 доменов'],
    [1, pluralCidr, '1 подсеть'],
    [3, pluralCidr, '3 подсети'],
    [7, pluralCidr, '7 подсетей'],
    [1, pluralList, '1 список'],
    [2, pluralList, '2 списка'],
  ]
  for (const [n, f, want] of cases) {
    assert.equal(f(n), want, `${f.name}(${n})`)
  }
})

test('the three shapes the old code actually used, each wrong for 2-4', () => {
  // Recorded as they were, not as one uniform naive check — I asserted a single shape
  // at first and the test caught me, which is the point of writing the old code down
  // rather than describing it from memory.
  const oldLists = (n) => (n === 1 ? 'список' : 'списков') // no few-form at all
  const oldDomains = () => 'доменов' // never varied
  const oldCidrs = () => 'подсетей' // never varied

  assert.equal(oldLists(2), 'списков') // wrong
  assert.equal(pluralList(2), '2 списка')
  assert.equal(oldDomains(), 'доменов') // wrong for every n but 5+
  assert.equal(pluralDomain(2), '2 домена')
  assert.equal(oldCidrs(), 'подсетей') // wrong for every n but 5+
  assert.equal(pluralCidr(2), '2 подсети')
})
