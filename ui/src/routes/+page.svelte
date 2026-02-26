<script>
  import { onMount, tick } from 'svelte';
  import { GatewayRPC, defaultGatewayURL } from '$lib/gateway';
  import Thread from '$lib/Thread.svelte';

  const PROVIDERS = [
    { id: '', name: 'Server Default' },
    { id: 'codex', name: 'Codex' },
    { id: 'openrouter', name: 'OpenRouter' },
    { id: 'mock', name: 'Mock' }
  ];

  const MODEL_PRESETS = [
    { provider: 'codex', model: 'gpt-5.2-codex', name: 'Codex 5.2' },
    { provider: 'codex', model: 'gpt-5.1-codex', name: 'Codex 5.1' },
    { provider: 'openrouter', model: 'minimax/minimax-m2', name: 'OpenRouter Minimax 2.5' },
    { provider: 'openrouter', model: 'moonshotai/kimi-k2', name: 'OpenRouter Kimi K2.5' }
  ];

  const BUILTIN_SLASH_COMMANDS = [
    {
      command: '/config',
      description: 'Change provider, model, and OpenRouter key.',
      source: 'built-in'
    },
    {
      command: '/new',
      description: 'Start a fresh conversation.',
      source: 'built-in'
    }
  ];

  let client = null;
  let destroyed = false;
  let reconnectTimer = null;
  let refreshTimer = null;

  let connected = false;
  let statusLabel = 'Connecting';
  let errorText = '';
  let gatewayURL = defaultGatewayURL();

  let sessions = [];
  let currentSessionID = '';
  let messages = [];
  let draft = '';
  let sending = false;

  let configOpen = false;
  let configSaving = false;
  let configNotice = '';
  let selectedProvider = '';
  let selectedModel = '';
  let hasOpenRouterKey = false;
  let openRouterKeyMasked = '';
  let openRouterKeyInput = '';
  let openRouterKeyDirty = false;

  let slashQuery = null;
  let slashMatches = [];
  let pluginSlashCommands = [];
  let availableSlashCommands = BUILTIN_SLASH_COMMANDS;

  let viewport = null;

  function isUser(role) {
    return role === 'user';
  }

  function sessionTitle(sessionID) {
    if (!sessionID) {
      return 'Untitled session';
    }
    const clean = String(sessionID).replace(/^s_/, '');
    return `Session ${clean.slice(0, 8)}`;
  }

  function formatWhen(iso) {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) {
      return '';
    }
    return d.toLocaleString([], {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit'
    });
  }

  function setDisconnected(label, err) {
    connected = false;
    statusLabel = label;
    if (err) {
      errorText = err instanceof Error ? err.message : String(err);
    }
  }

  function getSlashQuery(value) {
    const firstLine = String(value || '').split('\n')[0];
    const trimmed = firstLine.trimStart();
    if (!trimmed.startsWith('/')) {
      return null;
    }
    const token = trimmed.slice(1);
    if (token.includes(' ')) {
      return null;
    }
    return token.toLowerCase();
  }

  function normalizeConfig(result) {
    selectedProvider = String(result?.provider || '').trim();
    selectedModel = String(result?.model || '').trim();
    hasOpenRouterKey = Boolean(result?.has_openrouter_api_key);
    openRouterKeyMasked = String(result?.openrouter_api_key_masked || '').trim();
    openRouterKeyInput = '';
    openRouterKeyDirty = false;
  }

  function currentModelLabel() {
    if (!selectedProvider && !selectedModel) {
      return 'Server default';
    }
    const preset = MODEL_PRESETS.find(
      (item) => item.provider === selectedProvider && item.model === selectedModel
    );
    if (preset) {
      return preset.name;
    }
    if (selectedProvider && selectedModel) {
      return `${selectedProvider}#${selectedModel}`;
    }
    return selectedModel || `${selectedProvider} default`;
  }

  function filteredPresets() {
    if (!selectedProvider) {
      return MODEL_PRESETS;
    }
    return MODEL_PRESETS.filter((item) => item.provider === selectedProvider);
  }

  async function loadRuntimeConfig() {
    if (!client || !connected) {
      return;
    }
    try {
      const result = await client.call('system.config.get', {});
      normalizeConfig(result);
    } catch (err) {
      errorText = err instanceof Error ? err.message : String(err);
    }
  }

  async function loadPluginCommands() {
    if (!client || !connected) {
      return;
    }
    try {
      const result = await client.call('system.commands', {});
      const raw = Array.isArray(result?.commands) ? result.commands : [];
      pluginSlashCommands = raw
        .map((item) => ({
          command: String(item?.command || '').trim(),
          description: String(item?.description || '').trim(),
          source: String(item?.source || 'plugin').trim()
        }))
        .filter((item) => item.command.startsWith('/'));
    } catch (err) {
      // Plugin command discovery should not block chat.
      pluginSlashCommands = [];
    }
  }

  async function saveRuntimeConfig() {
    if (!client || !connected || configSaving) {
      return;
    }

    configSaving = true;
    configNotice = '';

    try {
      const params = {
        provider: selectedProvider,
        model: selectedModel
      };
      if (openRouterKeyDirty) {
        params.openrouter_api_key = openRouterKeyInput.trim();
      }

      const result = await client.call('system.config.set', params);
      normalizeConfig(result);
      configNotice = 'Saved runtime config.';
    } catch (err) {
      configNotice = err instanceof Error ? err.message : String(err);
    } finally {
      configSaving = false;
    }
  }

  async function clearOpenRouterKey() {
    openRouterKeyInput = '';
    openRouterKeyDirty = true;
    await saveRuntimeConfig();
  }

  function openConfig() {
    configOpen = true;
    configNotice = '';
    if (connected) {
      loadRuntimeConfig();
    }
  }

  function closeConfig() {
    configOpen = false;
    configNotice = '';
    openRouterKeyInput = '';
    openRouterKeyDirty = false;
  }

  function applyModelPreset(preset) {
    selectedProvider = preset.provider;
    selectedModel = preset.model;
  }

  function runSlashCommand(command) {
    if (command === '/config') {
      draft = '';
      openConfig();
      return true;
    }
    if (command === '/new') {
      startNewChat();
      draft = '';
      return true;
    }
    return false;
  }

  function pickSlashCommand(command) {
    if (runSlashCommand(command)) {
      return;
    }
    draft = `${command} `;
  }

  async function connectGateway() {
    if (destroyed) {
      return;
    }
    if (!client) {
      client = new GatewayRPC(gatewayURL);
    }
    statusLabel = 'Connecting';
    errorText = '';

    try {
      await client.connect({
        onOpen: () => {
          connected = true;
          statusLabel = 'Connected';
        },
        onClose: () => {
          if (destroyed) {
            return;
          }
          setDisconnected('Disconnected');
          scheduleReconnect();
        },
        onError: () => {
          if (destroyed) {
            return;
          }
          setDisconnected('Connection error');
        }
      });

      await client.call('system.ping', {});
      await loadPluginCommands();
      await loadRuntimeConfig();
      await refreshSessions();
      if (!currentSessionID && sessions.length > 0) {
        currentSessionID = sessions[0].id;
      }
      if (currentSessionID) {
        await refreshMessages();
      }
    } catch (err) {
      if (destroyed) {
        return;
      }
      setDisconnected('Reconnecting', err);
      scheduleReconnect();
    }
  }

  function scheduleReconnect() {
    if (reconnectTimer || destroyed) {
      return;
    }
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      connectGateway();
    }, 1500);
  }

  async function refreshSessions() {
    if (!client || !connected) {
      return;
    }
    try {
      const result = await client.call('session.list', { limit: 60 });
      sessions = Array.isArray(result?.sessions) ? result.sessions : [];
    } catch (err) {
      setDisconnected('Disconnected', err);
      scheduleReconnect();
    }
  }

  async function refreshMessages() {
    if (!client || !connected || !currentSessionID) {
      messages = [];
      return;
    }
    try {
      const result = await client.call('session.messages', {
        session_id: currentSessionID,
        limit: 200
      });
      messages = Array.isArray(result?.messages) ? result.messages : [];
    } catch (err) {
      setDisconnected('Disconnected', err);
      scheduleReconnect();
    }
  }

  async function selectSession(sessionID) {
    if (sessionID === currentSessionID) {
      return;
    }
    currentSessionID = sessionID;
    await refreshMessages();
  }

  async function sendMessage() {
    const text = draft.trim();
    if (!text || sending || !client || !connected) {
      return;
    }

    if (text.startsWith('/')) {
      const command = text.split(/\s+/, 1)[0].toLowerCase();
      if (runSlashCommand(command)) {
        return;
      }
    }

    sending = true;
    errorText = '';
    try {
      const result = await client.call('session.send', {
        session_id: currentSessionID,
        text
      });
      draft = '';
      currentSessionID = result?.session_id || currentSessionID;
      await refreshSessions();
      await refreshMessages();
      await tick();
      scrollToBottom();
    } catch (err) {
      errorText = err instanceof Error ? err.message : String(err);
    } finally {
      sending = false;
    }
  }

  function startNewChat() {
    currentSessionID = '';
    messages = [];
    draft = '';
    errorText = '';
  }

  function scrollToBottom() {
    if (!viewport) {
      return;
    }
    viewport.scrollTo({
      top: viewport.scrollHeight,
      behavior: 'smooth'
    });
  }

  $: slashQuery = getSlashQuery(draft);
  $: availableSlashCommands = [...BUILTIN_SLASH_COMMANDS, ...pluginSlashCommands];
  $: slashMatches =
    slashQuery === null
      ? []
      : availableSlashCommands.filter((item) => item.command.slice(1).startsWith(slashQuery));

  $: if (messages.length > 0) {
    tick().then(scrollToBottom);
  }

  onMount(() => {
    connectGateway();

    refreshTimer = setInterval(async () => {
      if (!connected) {
        return;
      }
      await refreshSessions();
      if (currentSessionID) {
        await refreshMessages();
      }
    }, 4000);

    return () => {
      destroyed = true;
      if (refreshTimer) {
        clearInterval(refreshTimer);
      }
      if (reconnectTimer) {
        clearTimeout(reconnectTimer);
      }
      if (client) {
        client.close();
      }
    };
  });
</script>

<svelte:head>
  <title>Elok Chat</title>
</svelte:head>

<div class="grid h-[100dvh] grid-rows-[auto_1fr] gap-3 overflow-hidden bg-[#0a0a0a] p-3 text-zinc-100 md:gap-4 md:p-4">
  <header class="flex items-center justify-between gap-4 rounded-2xl border border-white/[0.06] bg-[#121212]/90 px-4 py-3 shadow-[0_24px_64px_rgba(0,0,0,0.45)]">
    <div class="flex items-center gap-3.5">
      <div class="grid h-9 w-9 place-items-center rounded-xl bg-gradient-to-br from-white to-zinc-300 text-lg font-black text-black">
        e
      </div>
      <div>
        <h1 class="m-0 text-base font-semibold leading-none">Elok</h1>
        <p class="mt-1 font-mono text-[11px] text-zinc-500">local-first agent console</p>
      </div>
    </div>

    <div class="flex items-center gap-2.5">
      <div
        class={`inline-flex items-center gap-2 rounded-full border px-3 py-1 text-xs font-medium ${
          connected
            ? 'border-emerald-400/40 bg-emerald-500/10 text-emerald-300'
            : 'border-white/[0.08] bg-white/[0.02] text-zinc-500'
        }`}
      >
        <span
          class={`h-2 w-2 rounded-full ${
            connected ? 'bg-emerald-400 shadow-[0_0_0_4px_rgba(16,185,129,0.2)]' : 'bg-zinc-600'
          }`}
        ></span>
        <span>{statusLabel}</span>
      </div>
      <button
        type="button"
        class="rounded-lg border border-white/[0.08] px-3 py-2 text-sm font-medium text-zinc-300 transition hover:border-white/[0.14] hover:bg-white/[0.04] hover:text-zinc-100"
        onclick={openConfig}
      >
        Config
      </button>
      <button
        type="button"
        class="rounded-lg border border-white/[0.08] px-3 py-2 text-sm font-medium text-zinc-300 transition hover:border-white/[0.14] hover:bg-white/[0.04] hover:text-zinc-100"
        onclick={startNewChat}
      >
        New Chat
      </button>
    </div>
  </header>

  <main class="grid min-h-0 grid-cols-1 grid-rows-[auto_1fr] gap-3 md:grid-cols-[290px_1fr] md:grid-rows-1">
    <aside
      class="grid min-h-0 max-h-[9.3rem] grid-rows-[auto_1fr] overflow-hidden rounded-2xl border border-white/[0.06] bg-[#121212]/90 shadow-[0_24px_64px_rgba(0,0,0,0.45)] md:max-h-none"
    >
      <div class="flex items-baseline justify-between border-b border-white/[0.06] px-4 py-3.5">
        <h2 class="m-0 text-sm font-semibold tracking-[0.01em]">Sessions</h2>
        <small class="text-xs font-bold text-zinc-500">{sessions.length}</small>
      </div>

      {#if sessions.length === 0}
        <p class="m-0 px-4 py-4 text-sm text-zinc-500">No sessions yet. Send your first message.</p>
      {/if}

      <div class="flex flex-col gap-2 overflow-auto p-2 max-md:flex-row max-md:overflow-x-auto max-md:overflow-y-hidden max-md:pb-3">
        {#each sessions as session}
          <button
            type="button"
            class={`rounded-xl border px-3 py-2 text-left transition max-md:min-w-[11.2rem] ${
              session.id === currentSessionID
                ? 'border-white/[0.2] bg-white/[0.06]'
                : 'border-white/[0.06] bg-[#181818] hover:border-white/[0.14] hover:bg-[#1d1d1d]'
            }`}
            onclick={() => selectSession(session.id)}
          >
            <div class="flex items-baseline justify-between gap-3">
              <strong class="text-sm font-semibold leading-tight text-zinc-100">{sessionTitle(session.id)}</strong>
              <span class="whitespace-nowrap text-[11px] text-zinc-500"
                >{formatWhen(session.last_message_at)}</span
              >
            </div>
            <div class="mt-2 font-mono text-[11px] text-zinc-500">{session.id}</div>
          </button>
        {/each}
      </div>
    </aside>

    <section class="grid min-h-0 grid-rows-[auto_1fr_auto_auto] overflow-hidden rounded-2xl border border-white/[0.06] bg-[#121212]/90 shadow-[0_24px_64px_rgba(0,0,0,0.45)]">
      <div class="flex items-baseline justify-between gap-3 border-b border-white/[0.06] px-4 py-3.5">
        <h3 class="m-0 text-sm font-semibold">{currentSessionID ? sessionTitle(currentSessionID) : 'Fresh conversation'}</h3>
        <span class="max-w-[42%] truncate font-mono text-[11px] text-zinc-500 max-md:hidden"
          >{gatewayURL}</span
        >
      </div>

      <div class="flex min-h-0 flex-col gap-3 overflow-auto px-4 pb-2 pt-4" bind:this={viewport}>
        {#if messages.length === 0}
          <div class="m-auto max-w-[28rem] p-4 text-center text-zinc-500">
            <h4 class="mb-1 text-lg font-semibold text-zinc-200">Say something.</h4>
            <p class="m-0 text-sm leading-relaxed">
              Type `/` for commands. `/config` opens runtime model/provider setup.
            </p>
          </div>
        {:else}
          <Thread {messages} {formatWhen} />
        {/if}
      </div>

      <div class="border-t border-white/[0.06] px-3 pt-2">
        <div class="mb-2 flex items-center justify-between gap-2 text-[11px]">
          <button
            type="button"
            class="rounded-lg border border-white/[0.08] bg-white/[0.03] px-2.5 py-1 font-medium text-zinc-300 transition hover:border-white/[0.14] hover:bg-white/[0.05]"
            onclick={openConfig}
          >
            Model: {currentModelLabel()}
          </button>
          <span class="text-zinc-500">Type `/` for commands</span>
        </div>

        <div class="relative">
          {#if slashQuery !== null && slashMatches.length > 0}
            <div class="absolute bottom-full left-0 z-20 mb-2 w-full rounded-xl border border-white/[0.08] bg-[#171717] p-1 shadow-2xl">
              {#each slashMatches as command}
                <button
                  type="button"
                  class="flex w-full items-start justify-between gap-3 rounded-lg px-3 py-2 text-left transition hover:bg-white/[0.05]"
                  onclick={() => pickSlashCommand(command.command)}
                >
                  <span>
                    <span class="block font-mono text-xs text-zinc-200">{command.command}</span>
                    <span class="block text-[10px] text-zinc-600">{command.source}</span>
                  </span>
                  <span class="text-xs text-zinc-500">{command.description}</span>
                </button>
              {/each}
            </div>
          {/if}

          <form
            class="grid grid-cols-[1fr_auto] items-end gap-2.5 pb-3"
            onsubmit={(event) => {
              event.preventDefault();
              sendMessage();
            }}
          >
            <textarea
              bind:value={draft}
              class="min-h-12 max-h-40 resize-y rounded-xl border border-white/[0.08] bg-[#151515] px-3 py-2 text-sm text-zinc-100 outline-none transition focus:border-white/[0.18] focus:ring-4 focus:ring-white/10 disabled:cursor-not-allowed disabled:opacity-60"
              rows="2"
              placeholder={connected ? 'Message Elok...' : 'Waiting for gateway connection...'}
              disabled={!connected || sending}
              onkeydown={(event) => {
                if (event.key === 'Enter' && !event.shiftKey) {
                  event.preventDefault();
                  sendMessage();
                }
              }}
            ></textarea>
            <button
              type="submit"
              class="min-w-[6.3rem] rounded-xl bg-white px-4 py-2 text-sm font-semibold text-black transition hover:bg-zinc-200 disabled:cursor-not-allowed disabled:opacity-50"
              disabled={!connected || sending || draft.trim().length === 0}
            >
              {sending ? 'Sending...' : 'Send'}
            </button>
          </form>
        </div>
      </div>

      {#if errorText}
        <p class="m-0 px-4 pb-4 text-sm font-semibold text-red-400">{errorText}</p>
      {/if}
    </section>
  </main>
</div>

{#if configOpen}
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4"
    role="presentation"
    onclick={(event) => {
      if (event.target === event.currentTarget) {
        closeConfig();
      }
    }}
  >
    <section class="w-full max-w-xl rounded-2xl border border-white/[0.08] bg-[#121212] p-4 shadow-2xl">
      <div class="mb-4 flex items-center justify-between">
        <div>
          <h4 class="m-0 text-base font-semibold text-zinc-100">Runtime Config</h4>
          <p class="m-0 mt-1 text-xs text-zinc-500">Changes are persisted and applied immediately.</p>
        </div>
        <button
          type="button"
          class="rounded-lg border border-white/[0.08] px-2.5 py-1 text-xs text-zinc-300 hover:bg-white/[0.05]"
          onclick={closeConfig}
        >
          Close
        </button>
      </div>

      <div class="space-y-4">
        <div>
          <label for="runtime-provider" class="mb-1 block text-xs font-semibold uppercase tracking-wide text-zinc-500"
            >Provider</label
          >
          <select
            id="runtime-provider"
            bind:value={selectedProvider}
            class="w-full rounded-xl border border-white/[0.08] bg-[#171717] px-3 py-2 text-sm text-zinc-100 outline-none focus:border-white/[0.16]"
          >
            {#each PROVIDERS as provider}
              <option value={provider.id}>{provider.name}</option>
            {/each}
          </select>
        </div>

        <div>
          <label for="runtime-model" class="mb-1 block text-xs font-semibold uppercase tracking-wide text-zinc-500"
            >Model</label
          >
          <input
            id="runtime-model"
            bind:value={selectedModel}
            class="w-full rounded-xl border border-white/[0.08] bg-[#171717] px-3 py-2 text-sm text-zinc-100 outline-none focus:border-white/[0.16]"
            placeholder="gpt-5.2-codex or minimax/minimax-m2"
          />
          <div class="mt-2 grid grid-cols-1 gap-2 sm:grid-cols-2">
            {#each filteredPresets() as preset}
              <button
                type="button"
                class={`rounded-lg border px-3 py-2 text-left text-xs transition ${
                  selectedProvider === preset.provider && selectedModel === preset.model
                    ? 'border-white/[0.25] bg-white/[0.08] text-zinc-100'
                    : 'border-white/[0.08] bg-white/[0.02] text-zinc-400 hover:bg-white/[0.05]'
                }`}
                onclick={() => applyModelPreset(preset)}
              >
                <span class="block font-semibold">{preset.name}</span>
                <span class="font-mono text-[10px] text-zinc-500">{preset.provider}#{preset.model}</span>
              </button>
            {/each}
          </div>
        </div>

        <div>
          <label
            for="runtime-openrouter-key"
            class="mb-1 block text-xs font-semibold uppercase tracking-wide text-zinc-500"
            >OpenRouter Key</label
          >
          <input
            id="runtime-openrouter-key"
            value={openRouterKeyInput}
            oninput={(event) => {
              openRouterKeyInput = event.currentTarget.value;
              openRouterKeyDirty = true;
            }}
            class="w-full rounded-xl border border-white/[0.08] bg-[#171717] px-3 py-2 text-sm text-zinc-100 outline-none focus:border-white/[0.16]"
            placeholder={hasOpenRouterKey ? `Current: ${openRouterKeyMasked}` : 'sk-or-v1-...'}
            type="password"
          />
          <div class="mt-2 flex items-center justify-between gap-3 text-xs text-zinc-500">
            <span>
              {hasOpenRouterKey
                ? `Stored key: ${openRouterKeyMasked}`
                : 'No OpenRouter key stored in runtime config.'}
            </span>
            <button
              type="button"
              class="rounded-md border border-white/[0.08] px-2 py-1 text-zinc-300 hover:bg-white/[0.05]"
              onclick={clearOpenRouterKey}
              disabled={configSaving || !hasOpenRouterKey}
            >
              Clear Key
            </button>
          </div>
        </div>

        <div class="rounded-xl border border-white/[0.06] bg-[#171717] p-3 text-xs text-zinc-500">
          Codex auth is supported via your local `~/.codex/auth.json` subscription login when provider is
          `codex`.
        </div>

        <div class="flex items-center justify-between gap-3">
          <p class="m-0 text-xs text-zinc-500">{configNotice}</p>
          <button
            type="button"
            class="rounded-xl bg-white px-4 py-2 text-sm font-semibold text-black transition hover:bg-zinc-200 disabled:cursor-not-allowed disabled:opacity-60"
            onclick={saveRuntimeConfig}
            disabled={configSaving || !connected}
          >
            {configSaving ? 'Saving...' : 'Save Config'}
          </button>
        </div>
      </div>
    </section>
  </div>
{/if}
