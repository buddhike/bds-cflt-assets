<script>
  export let name
  export let service // { unhealthyRate, isAnomaly, statusCounts, totalCount }
</script>

<div class="tile" class:anomaly={service.isAnomaly}>
  <div class="tile-header">
    <h3>{name}</h3>
    <span class="badge {service.isAnomaly ? 'anomaly' : 'normal'}">{service.isAnomaly ? 'Anomaly' : 'Normal'}</span>
  </div>

  <div class="rate-row">
    <span class="rate">{(service.unhealthyRate * 100).toFixed(1)}% unhealthy</span>
  </div>

  {#if Object.keys(service.statusCounts).length > 0}
    <div class="status-counts">
      {#each Object.entries(service.statusCounts) as [code, count] (code)}
        <span class="status-badge status-{code}">{code}: {count}</span>
      {/each}
    </div>
  {/if}
</div>

<style>
  .tile {
    border: 1px solid rgba(255, 255, 255, 0.15);
    border-radius: 6px;
    padding: 0.5rem 0.6rem;
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    background: rgba(255, 255, 255, 0.03);
  }

  .tile.anomaly {
    border-color: #ff5c5c;
  }

  .tile-header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 0.4rem;
  }

  h3 {
    margin: 0;
    font-size: 0.7rem;
    line-height: 1.2;
    word-break: break-word;
  }

  .badge {
    flex-shrink: 0;
    font-size: 0.55rem;
    font-weight: 700;
    letter-spacing: 0.02em;
    padding: 0.1rem 0.3rem;
    border-radius: 3px;
    text-transform: uppercase;
    white-space: nowrap;
  }

  .badge.normal {
    background: rgba(80, 220, 140, 0.25);
    color: #7ee2ab;
  }

  .badge.anomaly {
    background: rgba(255, 92, 92, 0.3);
    color: #ff9a9a;
  }

  .rate-row {
    font-family: monospace;
    font-size: 0.7rem;
  }

  .status-counts {
    display: flex;
    flex-wrap: wrap;
    gap: 0.2rem;
  }

  .status-badge {
    font-size: 0.6rem;
    padding: 0.05rem 0.3rem;
    border-radius: 3px;
    background: rgba(255, 255, 255, 0.1);
  }

  .status-badge.status-500,
  .status-badge.status-502 {
    background: rgba(255, 92, 92, 0.3);
  }

  .status-badge.status-403,
  .status-badge.status-404 {
    background: rgba(255, 200, 80, 0.3);
  }
</style>
