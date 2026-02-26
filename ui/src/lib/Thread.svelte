<script>
  export let messages = [];
  export let formatWhen = () => '';

  function isUser(role) {
    return role === 'user';
  }

  function roleLabel(role) {
    const normalized = String(role || '').trim().toLowerCase();
    if (normalized === 'assistant') {
      return 'assistant';
    }
    if (normalized === 'user') {
      return 'you';
    }
    return normalized || 'assistant';
  }
</script>

<div class="space-y-1">
  {#each messages as message (message.id)}
    {#if isUser(message.role)}
      <article class="flex items-start gap-3 px-2 py-2">
        <div
          class="mt-1 grid h-7 w-7 shrink-0 place-items-center rounded-full border border-white/[0.12] bg-zinc-800 text-[10px] font-bold uppercase text-zinc-300"
        >
          u
        </div>
        <div class="min-w-0 flex-1 rounded-2xl border border-white/[0.1] bg-[#1e1e1e]/70 px-4 py-3 shadow-sm">
          <p class="m-0 whitespace-pre-wrap break-words text-[14px] font-medium leading-relaxed text-zinc-100">
            {message.content || ''}
          </p>
          <p class="mt-2 text-[10px] text-zinc-500">{formatWhen(message.created_at)}</p>
        </div>
      </article>
    {:else}
      <article class="px-11 py-2 pr-2">
        <div class="mb-1.5 flex items-center gap-2 text-[10px] uppercase tracking-widest text-zinc-500">
          <span class="font-semibold">{roleLabel(message.role)}</span>
          <span class="normal-case tracking-normal">{formatWhen(message.created_at)}</span>
        </div>
        <p class="m-0 whitespace-pre-wrap break-words text-[15px] leading-relaxed text-zinc-100">
          {message.content || ''}
        </p>
      </article>
    {/if}
  {/each}
</div>
