<script>
  import { onMount } from 'svelte'
  import { GetState } from './api.js'
  import MainView from './MainView.svelte'
  import Notice from './Notice.svelte'

  let state = null
  let error = ''

  onMount(async () => {
    try {
      state = await GetState()
    } catch (err) {
      error = String(err)
    }
  })
</script>

{#if state}
  <MainView summary={state} aiReports={state.aiReports} />
{:else if error}
  <main><div class="gate"><Notice tone="danger" alert>{error}</Notice></div></main>
{:else}
  <main><div class="gate"><p class="muted">Loading…</p></div></main>
{/if}
