<script>
  import { onMount, tick } from 'svelte';
  import { GatewayRPC, defaultGatewayURL } from '$lib/gateway';

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

<div class="shell">
  <header class="topbar">
    <div class="brand">
      <div class="mark">e</div>
      <div>
        <h1>Elok</h1>
        <p class="mono">local-first agent console</p>
      </div>
    </div>

    <div class="meta">
      <div class:status-online={connected} class="status-pill">
        <span class="dot"></span>
        <span>{statusLabel}</span>
      </div>
      <button class="ghost" onclick={startNewChat}>New Chat</button>
    </div>
  </header>

  <main class="workspace">
    <aside class="sessions">
      <div class="sessions-head">
        <h2>Sessions</h2>
        <small>{sessions.length}</small>
      </div>

      {#if sessions.length === 0}
        <p class="empty-sessions">No sessions yet. Send your first message.</p>
      {/if}

      <div class="session-list">
        {#each sessions as session}
          <button
            class:selected={session.id === currentSessionID}
            class="session-item"
            onclick={() => selectSession(session.id)}
          >
            <div class="session-row">
              <strong>{sessionTitle(session.id)}</strong>
              <span class="session-time">{formatWhen(session.last_message_at)}</span>
            </div>
            <div class="session-id mono">{session.id}</div>
          </button>
        {/each}
      </div>
    </aside>

    <section class="chat">
      <div class="chat-head">
        <h3>{currentSessionID ? sessionTitle(currentSessionID) : 'Fresh conversation'}</h3>
        <span class="mono url">{gatewayURL}</span>
      </div>

      <div class="message-stream" bind:this={viewport}>
        {#if messages.length === 0}
          <div class="empty-chat">
            <h4>Say something.</h4>
            <p>
              This UI speaks to `session.send`, `session.list`, and `session.messages` over your
              existing gateway.
            </p>
          </div>
        {/if}

        {#each messages as message}
          <article class:user={isUser(message.role)} class="bubble">
            <div class="bubble-meta">
              <span class="role mono">{message.role}</span>
              <span class="time">{formatWhen(message.created_at)}</span>
            </div>
            <p>{message.content}</p>
          </article>
        {/each}
      </div>

      <form
        class="composer"
        onsubmit={(event) => {
          event.preventDefault();
          sendMessage();
        }}
      >
        <textarea
          bind:value={draft}
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
        <button type="submit" disabled={!connected || sending || draft.trim().length === 0}>
          {sending ? 'Sending...' : 'Send'}
        </button>
      </form>

      {#if errorText}
        <p class="error">{errorText}</p>
      {/if}
    </section>
  </main>
</div>

<style>
  .shell {
    height: 100dvh;
    padding: 1rem;
    display: grid;
    grid-template-rows: auto 1fr;
    gap: 0.9rem;
  }

  .topbar {
    background: color-mix(in srgb, var(--paper) 87%, white);
    border: 1px solid var(--line);
    border-radius: 1rem;
    box-shadow: var(--shadow);
    padding: 0.85rem 1rem;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
  }

  .brand {
    display: flex;
    align-items: center;
    gap: 0.85rem;
  }

  .mark {
    width: 2.2rem;
    height: 2.2rem;
    border-radius: 0.7rem;
    background: linear-gradient(150deg, var(--brand), color-mix(in srgb, var(--brand) 65%, black));
    color: var(--brand-ink);
    font-weight: 800;
    display: grid;
    place-items: center;
  }

  h1 {
    margin: 0;
    font-size: 1.05rem;
    line-height: 1.1;
  }

  .brand p {
    margin: 0.05rem 0 0;
    color: var(--muted);
    font-size: 0.73rem;
  }

  .meta {
    display: flex;
    align-items: center;
    gap: 0.6rem;
  }

  .status-pill {
    display: inline-flex;
    align-items: center;
    gap: 0.45rem;
    font-size: 0.8rem;
    padding: 0.35rem 0.65rem;
    border-radius: 999px;
    border: 1px solid var(--line);
    color: var(--muted);
    background: color-mix(in srgb, var(--paper) 92%, white);
  }

  .status-pill .dot {
    width: 0.5rem;
    height: 0.5rem;
    border-radius: 100%;
    background: #a7a7a7;
  }

  .status-online {
    color: color-mix(in srgb, var(--brand) 70%, black);
    border-color: color-mix(in srgb, var(--brand) 40%, white);
    background: color-mix(in srgb, var(--brand) 9%, white);
  }

  .status-online .dot {
    background: var(--brand);
    box-shadow: 0 0 0 5px color-mix(in srgb, var(--brand) 23%, transparent);
  }

  .ghost {
    border: 1px solid color-mix(in srgb, var(--ink) 16%, white);
    background: transparent;
    color: color-mix(in srgb, var(--ink) 75%, black);
    border-radius: 0.65rem;
    padding: 0.45rem 0.7rem;
    font-weight: 600;
    cursor: pointer;
  }

  .ghost:hover {
    background: color-mix(in srgb, var(--paper) 75%, white);
  }

  .workspace {
    min-height: 0;
    display: grid;
    grid-template-columns: 290px 1fr;
    gap: 0.9rem;
  }

  .sessions,
  .chat {
    min-height: 0;
    background: color-mix(in srgb, var(--paper) 89%, white);
    border: 1px solid var(--line);
    border-radius: 1rem;
    box-shadow: var(--shadow);
    overflow: hidden;
  }

  .sessions {
    display: grid;
    grid-template-rows: auto 1fr;
  }

  .sessions-head {
    border-bottom: 1px solid var(--line);
    padding: 0.85rem 0.95rem;
    display: flex;
    align-items: baseline;
    justify-content: space-between;
  }

  .sessions-head h2 {
    margin: 0;
    font-size: 0.92rem;
    letter-spacing: 0.01em;
  }

  .sessions-head small {
    color: var(--muted);
    font-weight: 700;
  }

  .empty-sessions {
    margin: 0;
    padding: 1rem;
    color: var(--muted);
    font-size: 0.88rem;
  }

  .session-list {
    overflow: auto;
    padding: 0.55rem;
    display: flex;
    flex-direction: column;
    gap: 0.45rem;
  }

  .session-item {
    border: 1px solid color-mix(in srgb, var(--line) 75%, white);
    border-radius: 0.8rem;
    padding: 0.6rem;
    text-align: left;
    background: color-mix(in srgb, var(--paper) 92%, white);
    cursor: pointer;
  }

  .session-item:hover {
    border-color: color-mix(in srgb, var(--brand) 30%, white);
  }

  .session-item.selected {
    border-color: color-mix(in srgb, var(--brand) 46%, white);
    background: color-mix(in srgb, var(--brand) 8%, white);
  }

  .session-row {
    display: flex;
    justify-content: space-between;
    gap: 0.7rem;
    align-items: baseline;
  }

  .session-row strong {
    font-size: 0.84rem;
    line-height: 1.1;
    color: var(--ink);
  }

  .session-time {
    font-size: 0.7rem;
    color: var(--muted);
    white-space: nowrap;
  }

  .session-id {
    margin-top: 0.45rem;
    color: color-mix(in srgb, var(--muted) 90%, white);
    font-size: 0.68rem;
  }

  .chat {
    display: grid;
    grid-template-rows: auto 1fr auto auto;
  }

  .chat-head {
    border-bottom: 1px solid var(--line);
    padding: 0.85rem 1rem;
    display: flex;
    justify-content: space-between;
    gap: 0.8rem;
    align-items: baseline;
  }

  .chat-head h3 {
    margin: 0;
    font-size: 0.95rem;
  }

  .url {
    font-size: 0.7rem;
    color: var(--muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 42%;
  }

  .message-stream {
    min-height: 0;
    overflow: auto;
    padding: 1rem 1rem 0.6rem;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  .empty-chat {
    margin: auto;
    text-align: center;
    max-width: 28rem;
    color: var(--muted);
    padding: 1rem;
  }

  .empty-chat h4 {
    margin: 0 0 0.35rem;
    color: color-mix(in srgb, var(--ink) 80%, black);
    font-size: 1.1rem;
  }

  .empty-chat p {
    margin: 0;
    font-size: 0.92rem;
    line-height: 1.45;
  }

  .bubble {
    align-self: flex-start;
    max-width: min(46rem, 88%);
    border-radius: 0.9rem;
    padding: 0.65rem 0.75rem;
    border: 1px solid color-mix(in srgb, var(--line) 70%, white);
    background: var(--assistant-bubble);
    color: var(--assistant-ink);
  }

  .bubble.user {
    align-self: flex-end;
    background: var(--user-bubble);
    color: var(--user-ink);
    border-color: color-mix(in srgb, var(--user-bubble) 70%, black);
  }

  .bubble-meta {
    display: flex;
    align-items: baseline;
    gap: 0.6rem;
    margin-bottom: 0.42rem;
  }

  .role {
    font-size: 0.67rem;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    opacity: 0.84;
  }

  .time {
    font-size: 0.69rem;
    opacity: 0.72;
  }

  .bubble p {
    margin: 0;
    white-space: pre-wrap;
    word-break: break-word;
    line-height: 1.42;
    font-size: 0.94rem;
  }

  .composer {
    border-top: 1px solid var(--line);
    padding: 0.85rem 0.95rem;
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 0.65rem;
    align-items: end;
  }

  textarea {
    min-height: 3rem;
    max-height: 10rem;
    resize: vertical;
    border-radius: 0.75rem;
    border: 1px solid color-mix(in srgb, var(--line) 78%, white);
    background: color-mix(in srgb, var(--paper) 85%, white);
    color: var(--ink);
    padding: 0.65rem 0.75rem;
  }

  textarea:focus {
    border-color: color-mix(in srgb, var(--brand) 50%, white);
    outline: none;
    box-shadow: 0 0 0 4px color-mix(in srgb, var(--brand) 15%, transparent);
  }

  button[type='submit'] {
    border: none;
    border-radius: 0.75rem;
    background: linear-gradient(120deg, var(--brand), color-mix(in srgb, var(--brand) 65%, black));
    color: var(--brand-ink);
    font-weight: 700;
    padding: 0.62rem 0.95rem;
    min-width: 6.3rem;
    cursor: pointer;
  }

  button[type='submit']:disabled {
    cursor: not-allowed;
    opacity: 0.5;
  }

  .error {
    margin: 0;
    padding: 0 1rem 0.95rem;
    color: color-mix(in srgb, var(--accent) 88%, black);
    font-size: 0.86rem;
    font-weight: 600;
  }

  @media (max-width: 920px) {
    .shell {
      padding: 0.65rem;
      gap: 0.65rem;
    }

    .workspace {
      grid-template-columns: 1fr;
      grid-template-rows: auto 1fr;
    }

    .sessions {
      max-height: 9.3rem;
    }

    .session-list {
      flex-direction: row;
      overflow-x: auto;
      overflow-y: hidden;
      padding-bottom: 0.7rem;
    }

    .session-item {
      min-width: 11.2rem;
    }

    .url {
      display: none;
    }
  }
</style>
