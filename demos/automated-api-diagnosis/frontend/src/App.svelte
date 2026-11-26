<script>
  import { onMount } from 'svelte'
  import { GetState } from './api.js'
  import MainView from './MainView.svelte'

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
  <p class="message error">{error}</p>
{:else}
  <p class="message">Loading...</p>
{/if}

<style>
  .message {
    text-align: center;
    margin-top: 4rem;
    opacity: 0.7;
  }

  .error {
    color: #ff6b6b;
    opacity: 1;
  }
</style>
