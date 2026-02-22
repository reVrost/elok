function wsBaseURL() {
  if (typeof window === 'undefined') {
    return 'ws://127.0.0.1:7777/ws';
  }
  const scheme = window.location.protocol === 'https:' ? 'wss' : 'ws';
  return `${scheme}://${window.location.host}/ws`;
}

export class GatewayRPC {
  constructor(url = wsBaseURL()) {
    this.url = url;
    this.socket = null;
    this.seq = 0;
    this.pending = new Map();
    this.callbacks = {
      onOpen: null,
      onClose: null,
      onError: null
    };
  }

  connect(callbacks = {}) {
    this.callbacks = {
      ...this.callbacks,
      ...callbacks
    };

    if (this.socket && (this.socket.readyState === WebSocket.OPEN || this.socket.readyState === WebSocket.CONNECTING)) {
      return Promise.resolve();
    }

    return new Promise((resolve, reject) => {
      const socket = new WebSocket(this.url);
      this.socket = socket;

      const onOpen = () => {
        cleanup();
        if (this.callbacks.onOpen) {
          this.callbacks.onOpen();
        }
        resolve();
      };

      const onError = (event) => {
        cleanup();
        if (this.callbacks.onError) {
          this.callbacks.onError(event);
        }
        reject(new Error('websocket connection failed'));
      };

      const cleanup = () => {
        socket.removeEventListener('open', onOpen);
        socket.removeEventListener('error', onError);
      };

      socket.addEventListener('open', onOpen);
      socket.addEventListener('error', onError);

      socket.addEventListener('close', (event) => {
        this.rejectAllPending(new Error('gateway socket closed'));
        if (this.callbacks.onClose) {
          this.callbacks.onClose(event);
        }
      });

      socket.addEventListener('message', (event) => {
        this.handleMessage(event.data);
      });
    });
  }

  close() {
    if (!this.socket) {
      return;
    }
    this.socket.close();
    this.socket = null;
    this.rejectAllPending(new Error('gateway socket closed'));
  }

  async call(method, params = {}, timeoutMs = 30000) {
    if (!this.socket || this.socket.readyState !== WebSocket.OPEN) {
      throw new Error('gateway socket is not connected');
    }

    const id = `ui-${++this.seq}`;
    const payload = {
      type: 'call',
      id,
      method,
      params
    };

    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`rpc timeout for ${method}`));
      }, timeoutMs);

      this.pending.set(id, { resolve, reject, timer });
      try {
        this.socket.send(JSON.stringify(payload));
      } catch (err) {
        clearTimeout(timer);
        this.pending.delete(id);
        reject(err instanceof Error ? err : new Error(String(err)));
      }
    });
  }

  handleMessage(raw) {
    let message;
    try {
      message = JSON.parse(raw);
    } catch {
      return;
    }

    if (!message || !message.id || !this.pending.has(message.id)) {
      return;
    }

    const entry = this.pending.get(message.id);
    this.pending.delete(message.id);
    clearTimeout(entry.timer);

    if (message.type === 'error') {
      const err = message.error?.message || 'gateway error';
      entry.reject(new Error(err));
      return;
    }

    if (message.type === 'result') {
      entry.resolve(message.result ?? {});
      return;
    }

    entry.reject(new Error(`unexpected envelope type: ${message.type}`));
  }

  rejectAllPending(err) {
    for (const [id, entry] of this.pending.entries()) {
      clearTimeout(entry.timer);
      entry.reject(err);
      this.pending.delete(id);
    }
  }
}

export function defaultGatewayURL() {
  return wsBaseURL();
}
