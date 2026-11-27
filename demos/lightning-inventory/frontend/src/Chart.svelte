<script>
  // Latency of one scenario: p50, p95 and p99 over time, one point per second, from
  // CloudWatch. Series colours are the shared tokens --s1, --s2, --s3, in that order.
  let { title, series, windowMinutes, now } = $props()

  const W = 640
  const H = 210
  const M = { l: 46, r: 10, t: 8, b: 22 }
  const lines = [
    { key: 'p50', label: 'p50', token: '--s1' },
    { key: 'p95', label: 'p95', token: '--s2' },
    { key: 'p99', label: 'p99', token: '--s3' },
  ]

  // A gap longer than this many seconds breaks the line: no queries ran, or none finished.
  const GAP = 3

  function niceMax(v) {
    if (!(v > 0)) return 10
    const p = Math.pow(10, Math.floor(Math.log10(v)))
    for (const m of [1, 2, 5, 10]) if (v <= m * p) return m * p
    return 10 * p
  }

  const view = $derived.by(() => {
    const times = series?.times ?? []
    const end = now || (times.length ? times[times.length - 1] : 0)
    const start = end - windowMinutes * 60
    let top = 0
    for (const l of lines) for (const v of series?.[l.key] ?? []) top = Math.max(top, v)
    const max = niceMax(top)
    const x = (t) => M.l + ((t - start) / (end - start)) * (W - M.l - M.r)
    const y = (v) => M.t + (1 - v / max) * (H - M.t - M.b)
    const paths = lines.map((l) => {
      let d = ''
      let prev = null
      times.forEach((t, i) => {
        const v = series[l.key][i]
        d += `${prev !== null && t - prev <= GAP ? 'L' : 'M'}${x(t).toFixed(1)} ${y(v).toFixed(1)}`
        prev = t
      })
      return { ...l, d }
    })
    const yticks = [0, 0.25, 0.5, 0.75, 1].map((f) => ({ y: y(max * f), label: fmt(max * f) }))
    const xticks = [0, 0.25, 0.5, 0.75, 1].map((f) => {
      const t = start + (end - start) * f
      return { x: x(t), label: f === 1 ? 'now' : `-${fmtAge(end - t)}` }
    })
    const last = times.length ? times.length - 1 : -1
    return { paths, yticks, xticks, last }
  })

  function fmt(ms) {
    return ms >= 100 ? ms.toFixed(0) : ms >= 10 ? ms.toFixed(1) : ms.toFixed(2).replace(/0$/, '')
  }
  function fmtAge(s) {
    return s >= 60 ? `${Math.round(s / 60)}m` : `${Math.round(s)}s`
  }
  const latest = (key) => (view.last >= 0 ? series[key][view.last] : null)
  const empty = $derived(!series || !series.times || series.times.length === 0)
</script>

<section class="panel chart" aria-label={`${title} latency, p50, p95 and p99 in milliseconds, last ${windowMinutes} minutes`}>
  <div class="panel-head">
    <h3>{title}</h3>
    <ul class="legend">
      {#each lines as l}
        <li><span class="swatch" style={`background: var(${l.token})`}></span>{l.label}{#if latest(l.key) !== null}<b class="mono">&nbsp;{fmt(latest(l.key))} ms</b>{/if}</li>
      {/each}
    </ul>
  </div>
  <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label={`${title} latency chart`}>
    {#each view.yticks as t}
      <line class="gl" x1={M.l} x2={W - M.r} y1={t.y} y2={t.y} />
      <text class="axis" x={M.l - 6} y={t.y + 4} text-anchor="end">{t.label}</text>
    {/each}
    {#each view.xticks as t}
      <text class="axis" x={t.x} y={H - 6} text-anchor="middle">{t.label}</text>
    {/each}
    <text class="axis" x="4" y="10">ms</text>
    {#each view.paths as p}
      <path class="line" d={p.d} style={`stroke: var(${p.token})`} />
    {/each}
    {#if empty}
      <text class="axis" x={W / 2} y={H / 2} text-anchor="middle">No successful queries in this range yet</text>
    {/if}
  </svg>
</section>
