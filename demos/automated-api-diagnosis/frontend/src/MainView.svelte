<script>
  import Dashboard from './Dashboard.svelte'
  import Monitor from './Monitor.svelte'
  import Reports from './Reports.svelte'

  export let summary
  export let aiReports = true

  let tab = 'simulator'
</script>

<div class="shell">
  <nav>
    <button class:active={tab === 'simulator'} on:click={() => (tab = 'simulator')}>Simulator</button>
    <button class:active={tab === 'reports'} on:click={() => (tab = 'reports')}>AI Reports</button>
  </nav>

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

  nav {
    display: flex;
    gap: 0.25rem;
    padding: 0.4rem 0.8rem 0;
    border-bottom: 1px solid rgba(255, 255, 255, 0.15);
  }

  nav button {
    padding: 0.45rem 1rem;
    border: none;
    border-bottom: 2px solid transparent;
    background: none;
    color: inherit;
    font: inherit;
    font-size: 0.85rem;
    font-weight: 600;
    opacity: 0.6;
    cursor: pointer;
  }

  nav button:hover {
    opacity: 0.9;
  }

  nav button.active {
    opacity: 1;
    border-bottom-color: #4a9eff;
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
    width: 260px;
    flex-shrink: 0;
    border-left: 1px solid rgba(255, 255, 255, 0.15);
  }

  /* Must stay below .split/.reports-view: equal specificity, so the later
     rule wins over their display values. */
  .hidden {
    display: none;
  }
</style>
