<script>
  export let name
  export let service // { unhealthyRate, isAnomaly, statusCounts, totalCount }
</script>

<div class="health" class:danger={service.isAnomaly}>
  <header>
    <h3 class="mono">{name}</h3>
    <span class="pill" class:ok={!service.isAnomaly} class:danger={service.isAnomaly}>{service.isAnomaly ? 'Anomaly' : 'Normal'}</span>
  </header>

  <p class="mono small num">{(service.unhealthyRate * 100).toFixed(1)}% unhealthy</p>

  {#if Object.keys(service.statusCounts).length > 0}
    <div class="codes">
      {#each Object.entries(service.statusCounts) as [code, count] (code)}
        <span class="pill num" class:warn={code === '403' || code === '404'} class:danger={code === '500' || code === '502'}>{code}: {count}</span>
      {/each}
    </div>
  {/if}
</div>

<style>
  header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 0.4rem;
  }

  h3 {
    margin: 0;
    word-break: break-word;
  }

  p {
    margin: 0;
  }

  .codes {
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem;
  }
</style>
