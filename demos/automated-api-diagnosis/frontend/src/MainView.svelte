<script>
  import Dashboard from './Dashboard.svelte'
  import Monitor from './Monitor.svelte'
  import Reports from './Reports.svelte'
  import Logo from './Logo.svelte'

  export let summary
  export let aiReports = true

  let tab = 'simulator'
</script>

<div class="shell">
  <header class="topbar">
    <h1 class="brand"><Logo />Launchpad <span class="sep">/</span> Automated API diagnosis</h1>
    <nav class="tabs" aria-label="Views">
      <button class="tab" aria-current={tab === 'simulator' ? 'page' : undefined} on:click={() => (tab = 'simulator')}>Simulator</button>
      <button class="tab" aria-current={tab === 'reports' ? 'page' : undefined} on:click={() => (tab = 'reports')}>AI reports</button>
    </nav>
  </header>

  <!-- Both tabs stay mounted (inactive one is just hidden) so the report
       list keeps accumulating while the simulator tab is in front. -->
  <div class="content">
    <div class="split" class:hidden={tab !== 'simulator'}>
      <div class="pane simulator">
        <Dashboard {summary} />
      </div>
      <div class="pane monitor">
        <Monitor />
      </div>
    </div>

    <div class="reports-view" class:hidden={tab !== 'reports'}>
      <Reports {aiReports} />
    </div>
  </div>
</div>

<style>
  .shell {
    display: flex;
    flex-direction: column;
    height: 100%;
  }

  .shell > .topbar {
    flex: none;
    position: static;
  }

  .content {
    flex: 1;
    min-height: 0;
  }

  .split {
    display: flex;
    height: 100%;
  }

  .reports-view {
    height: 100%;
  }

  .pane {
    overflow-y: auto;
  }

  .simulator {
    flex: 1;
    min-width: 0;
  }

  .monitor {
    width: 17rem;
    flex-shrink: 0;
    background: var(--surface);
    border-left: 1px solid var(--border);
  }

  /* Must stay below .split/.reports-view: equal specificity, so the later
     rule wins over their display values. */
  .hidden {
    display: none;
  }
</style>
