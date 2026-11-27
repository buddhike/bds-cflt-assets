<script>
  // Anything a person must read: a left bar in the tone's colour, an icon and a bold lead-in.
  export let tone = 'info' // info | ok | warn | danger
  export let title = ''
  // Errors are announced as soon as they appear.
  export let alert = false
</script>

<div class="notice" class:ok={tone === 'ok'} class:warn={tone === 'warn'} class:danger={tone === 'danger'} role={alert ? 'alert' : undefined}>
  <svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" focusable="false">
    {#if tone === 'ok'}
      <circle cx="8" cy="8" r="6.5" /><path d="M5.2 8.2l2 2 3.6-4" />
    {:else if tone === 'warn'}
      <path d="M8 2.2 14.6 13.6H1.4z" /><path d="M8 6.6v3.2M8 11.7v.1" />
    {:else if tone === 'danger'}
      <circle cx="8" cy="8" r="6.5" /><path d="M5.8 5.8l4.4 4.4M10.2 5.8l-4.4 4.4" />
    {:else}
      <circle cx="8" cy="8" r="6.5" /><path d="M8 7.2v4M8 4.9v.1" />
    {/if}
  </svg>
  <div>{#if title}<b>{title}</b>{' '}{/if}<slot /></div>
</div>
