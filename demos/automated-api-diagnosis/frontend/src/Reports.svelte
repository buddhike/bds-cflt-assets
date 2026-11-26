<script>
  import { onMount } from 'svelte'
  import { marked } from 'marked'
  import DOMPurify from 'dompurify'
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

  function statusClass(code) {
    if (code === '500' || code === '502' || code === '503' || code === '504') return 'server-error'
    if (code === '403' || code === '404') return 'client-error'
    return ''
  }

  function agentClass(status) {
    return /fail|error/i.test(status) ? 'agent-failed' : ''
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
    <h2>AI anomaly reports</h2>

    {#if monitorError}
      <div class="error">
        {monitorError}
      </div>
    {/if}

    {#if reports.length === 0 && !aiReports}
      <p class="empty">
        No AI agent was deployed with this demo, so nothing writes to <code>api_anomaly_ai_reports</code>.
        Add the agent's statements under <code>tf/ai</code> (see its README) and launch the demo again.
      </p>
    {:else if reports.length === 0}
      <p class="empty">Waiting for reports on <code>api_anomaly_ai_reports</code>…</p>
    {:else}
      <ul>
        {#each reports as r (r.id)}
          <li>
            <button
              type="button"
              class="item"
              class:active={selected?.id === r.id}
              on:click={() => (selected = r)}
            >
              <span class="api">{r.api_id}</span>
              <div class="item-bottom">
                <div class="item-badges">
                  <span class="badge status {statusClass(r.http_status)}">{r.http_status}</span>
                  <span class="badge agent {agentClass(r.agent_status)}">{r.agent_status}</span>
                </div>
                <span class="time">{formatTime(r.detected_at)}</span>
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
          <h3>{selected.api_id}</h3>
          <div class="meta">
            <span class="badge status {statusClass(selected.http_status)}">{selected.http_status}</span>
            <span class="badge agent {agentClass(selected.agent_status)}">{selected.agent_status}</span>
            <span class="time">{formatTime(selected.detected_at)}</span>
          </div>
        </div>
        <button type="button" class="close" title="Close (Esc)" on:click={() => (selected = null)}>×</button>
      </header>

      {#if rendered}
        <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
        <div class="markdown" on:click={openLinksExternally}>{@html rendered}</div>
      {:else}
        <p class="empty">This report has no content.</p>
      {/if}
    </section>
  {/if}
</div>

<style>
  .reports {
    display: flex;
    height: 100%;
    text-align: left;
  }

  .list-pane {
    flex: 1;
    min-width: 0;
    overflow-y: auto;
    padding: 0.8rem 1rem;
    box-sizing: border-box;
  }

  .reports.split .list-pane {
    flex: 0 0 340px;
    border-right: 1px solid rgba(255, 255, 255, 0.15);
  }

  .detail-pane {
    flex: 1;
    min-width: 0;
    overflow-y: auto;
    padding: 0.8rem 1.5rem 2rem;
    box-sizing: border-box;
  }

  h2 {
    margin: 0 0 0.6rem;
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    opacity: 0.7;
  }

  .error {
    color: #ff6b6b;
    font-size: 0.8rem;
    margin-bottom: 0.6rem;
    word-break: break-word;
  }

  .hint {
    margin-top: 0.3rem;
    opacity: 0.75;
    color: white;
  }

  .empty {
    opacity: 0.6;
    font-size: 0.9rem;
  }

  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
  }

  .item {
    width: 100%;
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
    padding: 0.55rem 0.7rem;
    border: 1px solid rgba(255, 255, 255, 0.15);
    border-radius: 6px;
    background: rgba(255, 255, 255, 0.03);
    color: inherit;
    font: inherit;
    text-align: left;
    cursor: pointer;
  }

  .item:hover {
    background: rgba(255, 255, 255, 0.07);
  }

  .item.active {
    border-color: #4a9eff;
    background: rgba(74, 158, 255, 0.12);
  }

  .item-bottom {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 0.6rem;
  }

  .api {
    font-size: 0.8rem;
    font-weight: 700;
    word-break: break-word;
  }

  .time {
    flex-shrink: 0;
    font-size: 0.7rem;
    opacity: 0.65;
    font-family: monospace;
  }

  .item-badges,
  .meta {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.3rem;
  }

  .badge {
    font-size: 0.65rem;
    font-weight: 700;
    padding: 0.1rem 0.4rem;
    border-radius: 3px;
    background: rgba(255, 255, 255, 0.12);
    white-space: nowrap;
  }

  .badge.agent {
    text-transform: uppercase;
    letter-spacing: 0.02em;
    background: rgba(74, 158, 255, 0.2);
    color: #9ccaff;
  }

  .badge.status.server-error {
    background: rgba(255, 92, 92, 0.3);
    color: #ff9a9a;
  }

  .badge.status.client-error {
    background: rgba(255, 200, 80, 0.3);
    color: #ffd98a;
  }

  .badge.agent.agent-failed {
    background: rgba(255, 92, 92, 0.3);
    color: #ff9a9a;
  }

  header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 1rem;
    padding-bottom: 0.7rem;
    margin-bottom: 0.8rem;
    border-bottom: 1px solid rgba(255, 255, 255, 0.15);
  }

  h3 {
    margin: 0 0 0.35rem;
    font-size: 1.05rem;
    word-break: break-word;
  }

  .close {
    flex-shrink: 0;
    width: 1.8rem;
    height: 1.8rem;
    border: none;
    border-radius: 4px;
    background: rgba(255, 255, 255, 0.1);
    color: inherit;
    font-size: 1.2rem;
    line-height: 1;
    cursor: pointer;
  }

  .close:hover {
    background: rgba(255, 255, 255, 0.2);
  }

  /* {@html} output isn't touched by Svelte's scoped styles, hence :global. */
  .markdown {
    font-size: 0.9rem;
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
    font-size: 1.35rem;
  }

  .markdown :global(h2) {
    font-size: 1.15rem;
    text-transform: none;
    letter-spacing: normal;
    opacity: 1;
    padding-bottom: 0.25em;
    border-bottom: 1px solid rgba(255, 255, 255, 0.12);
  }

  .markdown :global(h3) {
    font-size: 1rem;
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

  .markdown :global(a) {
    color: #7bb8ff;
  }

  .markdown :global(code) {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.85em;
    padding: 0.1em 0.35em;
    border-radius: 3px;
    background: rgba(255, 255, 255, 0.1);
  }

  .markdown :global(pre) {
    padding: 0.7rem 0.9rem;
    border-radius: 6px;
    background: rgba(0, 0, 0, 0.3);
    overflow-x: auto;
  }

  .markdown :global(pre code) {
    padding: 0;
    background: none;
  }

  .markdown :global(blockquote) {
    padding: 0.1em 0.9em;
    border-left: 3px solid rgba(255, 255, 255, 0.25);
    opacity: 0.85;
  }

  .markdown :global(hr) {
    border: none;
    border-top: 1px solid rgba(255, 255, 255, 0.15);
    margin: 1.2em 0;
  }

  .markdown :global(table) {
    border-collapse: collapse;
    display: block;
    overflow-x: auto;
  }

  .markdown :global(th),
  .markdown :global(td) {
    padding: 0.35rem 0.7rem;
    border: 1px solid rgba(255, 255, 255, 0.15);
    text-align: left;
  }

  .markdown :global(th) {
    background: rgba(255, 255, 255, 0.07);
  }
</style>
