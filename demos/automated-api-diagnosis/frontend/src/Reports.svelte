<script>
  import { onMount } from 'svelte'
  import { marked } from 'marked'
  import DOMPurify from 'dompurify'
  import Notice from './Notice.svelte'
  import { GetReportBacklog, OpenURL, EventsOn, OnReconnect } from './api.js'

  // Whether Terraform deployed an AI agent to write the reports.
  export let aiReports = true

  // The consumer starts at the tail of api_anomaly_ai_reports, so the list
  // only holds reports that arrive while the app is open. Cap it so a
  // long-running session doesn't grow without bound (newest kept).
  const MAX_REPORTS = 500

  let reports = []
  let selected = null
  let monitorError = ''

  // Kept so ties on detected_at still order by which arrived last.
  let arrivalSeq = 0

  onMount(() => {
    // Subscribe before starting so no report is missed.
    const offEvent = EventsOn('report:event', addReport)
    const offError = EventsOn('report:error', (msg) => {
      monitorError = msg
    })
    const loadBacklog = () =>
      GetReportBacklog()
        .then((past) => past.forEach(addReport))
        .catch((err) => {
          monitorError = String(err)
        })
    loadBacklog()
    // Reports sent while the stream was down are gone: ask the server for them again.
    const offReconnect = OnReconnect(loadBacklog)
    return () => {
      offEvent()
      offError()
      offReconnect()
    }
  })

  function addReport(r) {
    monitorError = ''
    // The same report can arrive from the backlog and from the stream.
    if (reports.some((x) => x.id === r.id)) return
    reports = [...reports, { ...r, arrival: arrivalSeq++ }]
      .sort((a, b) => b.detected_at - a.detected_at || b.arrival - a.arrival)
      .slice(0, MAX_REPORTS)
  }

  function formatTime(ms) {
    return new Date(ms).toLocaleString()
  }

  // The tone of a pill: failures red, client errors amber, anything else neutral.
  function statusTone(code) {
    if (code === '500' || code === '502' || code === '503' || code === '504') return 'danger'
    if (code === '403' || code === '404') return 'warn'
    return ''
  }

  function agentTone(status) {
    return /fail|error/i.test(status) ? 'danger' : ''
  }

  function renderMarkdown(md) {
    return DOMPurify.sanitize(marked.parse(md, { gfm: true, async: false }))
  }

  // A plain <a> click would navigate away from the UI.
  function openLinksExternally(event) {
    const anchor = event.target.closest('a[href]')
    if (!anchor) return
    event.preventDefault()
    const href = anchor.getAttribute('href')
    if (/^https?:\/\//i.test(href)) OpenURL(href)
  }

  function onKeydown(event) {
    if (event.key === 'Escape') selected = null
  }

  $: rendered = selected ? renderMarkdown(selected.report) : ''
</script>

<svelte:window on:keydown={onKeydown} />

<div class="reports" class:split={selected}>
  <section class="list-pane">
    <h2 class="label">AI anomaly reports</h2>

    {#if monitorError}
      <div class="notice-row"><Notice tone="danger" alert>{monitorError}</Notice></div>
    {/if}

    {#if reports.length === 0 && !aiReports}
      <p class="muted small">
        No AI agent was deployed with this demo, so nothing writes to <code>api_anomaly_ai_reports</code>.
        Add the agent's statements under <code>tf/ai</code> (see its README) and launch the demo again.
      </p>
    {:else if reports.length === 0}
      <p class="muted small">Waiting for reports on <code>api_anomaly_ai_reports</code>…</p>
    {:else}
      <ul>
        {#each reports as r (r.id)}
          <li>
            <button
              type="button"
              class="item"
              aria-current={selected?.id === r.id ? 'true' : undefined}
              on:click={() => (selected = r)}
            >
              <span class="api mono">{r.api_id}</span>
              <div class="item-bottom">
                <div class="badges">
                  <span class="pill num {statusTone(r.http_status)}">{r.http_status}</span>
                  <span class="pill {agentTone(r.agent_status)}">{r.agent_status}</span>
                </div>
                <span class="time mono faint">{formatTime(r.detected_at)}</span>
              </div>
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </section>

  {#if selected}
    <section class="detail-pane">
      <header>
        <div>
          <h3 class="mono">{selected.api_id}</h3>
          <div class="badges">
            <span class="pill num {statusTone(selected.http_status)}">{selected.http_status}</span>
            <span class="pill {agentTone(selected.agent_status)}">{selected.agent_status}</span>
            <span class="time mono faint">{formatTime(selected.detected_at)}</span>
          </div>
        </div>
        <button type="button" class="quiet sm" aria-label="Close report" title="Close (Esc)" on:click={() => (selected = null)}>×</button>
      </header>

      {#if rendered}
        <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
        <div class="markdown" on:click={openLinksExternally}>{@html rendered}</div>
      {:else}
        <p class="muted small">This report has no content.</p>
      {/if}
    </section>
  {/if}
</div>

<style>
  .reports {
    display: flex;
    height: 100%;
  }

  .list-pane {
    flex: 1;
    min-width: 0;
    overflow-y: auto;
    padding: 0.8rem 1rem;
    box-sizing: border-box;
  }

  .reports.split .list-pane {
    flex: 0 0 21rem;
    border-right: 1px solid var(--border);
    background: var(--surface);
  }

  .detail-pane {
    flex: 1;
    min-width: 0;
    overflow-y: auto;
    padding: 0.8rem 1.5rem 2rem;
    box-sizing: border-box;
  }

  .list-pane h2 {
    margin: 0 0 0.6rem;
  }

  .notice-row {
    margin-bottom: 0.6rem;
  }

  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
  }

  .item {
    width: 100%;
    height: auto;
    display: flex;
    flex-direction: column;
    align-items: stretch;
    justify-content: flex-start;
    gap: 0.35rem;
    padding: 0.55rem 0.7rem;
    font-weight: 400;
    text-align: left;
    white-space: normal;
  }

  .item[aria-current='true'] {
    border-color: var(--accent);
    background: color-mix(in srgb, var(--accent) 10%, var(--surface));
  }

  .item-bottom {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 0.6rem;
  }

  .api {
    font-weight: 600;
    word-break: break-word;
  }

  .time {
    flex-shrink: 0;
    font-size: var(--fs-small);
  }

  .badges {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.3rem;
  }

  header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 1rem;
    padding-bottom: 0.7rem;
    margin-bottom: 0.8rem;
    border-bottom: 1px solid var(--border);
  }

  h3 {
    margin: 0 0 0.35rem;
    font-size: var(--fs-h2);
    word-break: break-word;
  }

  /* {@html} output isn't touched by Svelte's scoped styles, hence :global. */
  .markdown {
    line-height: 1.55;
    overflow-wrap: anywhere;
  }

  .markdown > :global(:first-child) {
    margin-top: 0;
  }

  .markdown :global(h1),
  .markdown :global(h2),
  .markdown :global(h3),
  .markdown :global(h4) {
    margin: 1.3em 0 0.5em;
    line-height: 1.25;
  }

  .markdown :global(h1) {
    font-size: var(--fs-h1);
  }

  .markdown :global(h2) {
    font-size: var(--fs-h2);
    padding-bottom: 0.25em;
    border-bottom: 1px solid var(--border);
  }

  .markdown :global(h3),
  .markdown :global(h4) {
    font-size: var(--fs-h3);
  }

  .markdown :global(p),
  .markdown :global(ul),
  .markdown :global(ol),
  .markdown :global(blockquote),
  .markdown :global(pre),
  .markdown :global(table) {
    margin: 0.7em 0;
  }

  .markdown :global(ul),
  .markdown :global(ol) {
    padding-left: 1.4rem;
  }

  .markdown :global(li) {
    margin: 0.2em 0;
  }

  .markdown :global(pre) {
    padding: 0.7rem 0.9rem;
    border-radius: var(--r-md);
    color: var(--code-text);
    background: var(--code-bg);
    border: 1px solid var(--border);
    overflow-x: auto;
  }

  .markdown :global(pre code) {
    color: inherit;
  }

  .markdown :global(blockquote) {
    padding: 0.1em 0.9em;
    border-left: 3px solid var(--border-strong);
    color: var(--text-2);
  }

  .markdown :global(table) {
    border-collapse: collapse;
    display: block;
    overflow-x: auto;
  }

  .markdown :global(th),
  .markdown :global(td) {
    padding: 0.35em 0.7em;
    border: 1px solid var(--border);
    text-align: left;
  }

  .markdown :global(th) {
    background: var(--surface-2);
  }

  .markdown :global(hr) {
    border: 0;
    border-top: 1px solid var(--border);
  }
</style>
