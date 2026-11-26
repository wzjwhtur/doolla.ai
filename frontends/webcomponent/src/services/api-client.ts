/**
 * API client for communicating with Doolla Agents backend
 */

export interface ChatMessage {
  id: string;
  content: string;
  type: 'user' | 'assistant';
  timestamp: number;
}

export interface ChatRequest {
  message: string;
  conversation_id?: string;
  user_id?: string;
  request_id?: string;
  metadata?: Record<string, any>;
}

export interface ChatStreamChunk {
  rich: Record<string, any>;
  simple?: Record<string, any>;
  conversation_id: string;
  request_id: string;
  timestamp: number;
}

export interface ChatResponse {
  chunks: ChatStreamChunk[];
  conversation_id: string;
  request_id: string;
  total_chunks: number;
}

export interface ApiClientConfig {
  baseUrl?: string;
  sseEndpoint?: string;
  wsEndpoint?: string;
  pollEndpoint?: string;
  timeout?: number;
  customHeaders?: Record<string, string>;
}

export class DoollaApiClient {
  public readonly baseUrl: string;
  private sseEndpoint: string;
  private wsEndpoint: string;
  private pollEndpoint: string;
  private timeout: number;
  private customHeaders: Record<string, string>;

  constructor(config: ApiClientConfig = {}) {
    this.baseUrl = config.baseUrl || '';
    this.sseEndpoint = config.sseEndpoint || '/api/doolla/v2/chat_sse';
    this.wsEndpoint = config.wsEndpoint || '/api/doolla/v2/chat_websocket';
    this.pollEndpoint = config.pollEndpoint || '/api/doolla/v2/chat_poll';
    this.timeout = config.timeout || 30000;
    this.customHeaders = config.customHeaders || {};

    console.log('[DoollaApiClient] Constructor called with config:', config);
    console.log('[DoollaApiClient] Endpoint configuration:');
    console.log('  - SSE endpoint:', this.sseEndpoint, config.sseEndpoint ? '(custom)' : '(default)');
    console.log('  - WS endpoint:', this.wsEndpoint, config.wsEndpoint ? '(custom)' : '(default)');
    console.log('  - Poll endpoint:', this.pollEndpoint, config.pollEndpoint ? '(custom)' : '(default)');
    console.log('  - Base URL:', this.baseUrl || '(empty)');
  }

  /**
   * Update custom headers (e.g., for authentication)
   */
  setCustomHeaders(headers: Record<string, string>) {
    this.customHeaders = headers;
  }

  /**
   * Get current custom headers
   */
  getCustomHeaders(): Record<string, string> {
    return { ...this.customHeaders };
  }

  /**
   * Send message using Server-Sent Events (SSE) streaming
   */
  async *streamChat(request: ChatRequest): AsyncGenerator<ChatStreamChunk, void, unknown> {
    const url = this.sseEndpoint.startsWith('http')
      ? this.sseEndpoint
      : `${this.baseUrl}${this.sseEndpoint}`;

    console.log('[DoollaApiClient] SSE streaming to URL:', url);
    console.log('[DoollaApiClient] SSE endpoint config:', {
      baseUrl: this.baseUrl,
      sseEndpoint: this.sseEndpoint,
      constructedUrl: url
    });

    const response = await fetch(url, {
      method: 'POST',
      credentials: 'include',
      headers: {
        'Content-Type': 'application/json',
        'Accept': 'text/event-stream',
        ...this.customHeaders,
      },
      body: JSON.stringify(request),
    });

    if (!response.ok) {
      throw new Error(`HTTP ${response.status}: ${response.statusText}`);
    }

    const reader = response.body?.getReader();
    if (!reader) {
      throw new Error('No response body');
    }

    const decoder = new TextDecoder();
    let buffer = '';
    const dataLines: string[] = [];

    try {
      while (true) {
        const { done, value } = await reader.read();
        if (done) throw new Error('SSE connection closed before [DONE]');

        buffer += decoder.decode(value, { stream: true });
        const lines = buffer.split('\n');
        buffer = lines.pop()!;
        for (const rawLine of lines) {
          const line = rawLine.replace(/\r$/, '');
          if (line.startsWith('data:')) {
            dataLines.push(line.slice(5).replace(/^ /, ''));
          } else if (line === '' && dataLines.length > 0) {
            const data = dataLines.join('\n');
            dataLines.length = 0;
            if (data === '[DONE]') return;
            yield JSON.parse(data) as ChatStreamChunk;
          }
        }
      }
    } finally {
      try {
        await reader.cancel();
      } finally {
        reader.releaseLock();
      }
    }
  }

  /**
   * Send message using WebSocket
   */
  createWebSocketConnection(): Promise<WebSocket> {
    return new Promise((resolve, reject) => {
      let wsUrl: string;

      if (this.wsEndpoint.startsWith('ws://') || this.wsEndpoint.startsWith('wss://')) {
        // Absolute WebSocket URL provided
        wsUrl = this.wsEndpoint;
      } else {
        // Relative path - construct from baseUrl
        if (this.baseUrl) {
          // Parse baseUrl to extract host and convert http(s) to ws(s)
          const baseUrlObj = new URL(this.baseUrl);
          const wsProtocol = baseUrlObj.protocol === 'https:' ? 'wss:' : 'ws:';
          wsUrl = `${wsProtocol}//${baseUrlObj.host}${this.wsEndpoint}`;
        } else {
          // Fallback to window.location
          const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
          wsUrl = `${protocol}//${window.location.host}${this.wsEndpoint}`;
        }
      }

      const ws = new WebSocket(wsUrl);

      const cleanup = () => {
        clearTimeout(timer);
        ws.removeEventListener('open', onOpen);
        ws.removeEventListener('error', onFailure);
        ws.removeEventListener('close', onFailure);
      };
      const onOpen = () => {
        cleanup();
        resolve(ws);
      };
      const onFailure = () => {
        cleanup();
        reject(new Error('WebSocket connection failed'));
      };
      const timer = setTimeout(() => {
        cleanup();
        ws.close();
        reject(new Error('WebSocket connection timeout'));
      }, this.timeout);
      ws.addEventListener('open', onOpen);
      ws.addEventListener('error', onFailure);
      ws.addEventListener('close', onFailure);
    });
  }

  /**
   * Send immediately and buffer incoming frames until the consumer is ready.
   */
  async sendWebSocketMessage(
    ws: WebSocket,
    request: ChatRequest
  ): Promise<AsyncGenerator<ChatStreamChunk, void, unknown>> {
    if (ws.readyState !== WebSocket.OPEN) throw new Error('WebSocket not connected');

    const requestId = request.request_id || this.generateId();
    let cleanup: () => void;
    const stream = new ReadableStream<ChatStreamChunk | Error>({
      start(controller) {
        const finish = (error?: Error) => {
          cleanup();
          // Keep earlier chunks readable when a later frame reports a failure.
          if (error) controller.enqueue(error);
          controller.close();
        };
        const onClose = () => finish(new Error('WebSocket closed before completion'));
        const onError = () => finish(new Error('WebSocket connection error'));
        const onMessage = (event: MessageEvent) => {
          try {
            const frame = JSON.parse(event.data);
            if (frame.request_id && frame.request_id !== requestId) return;
            const type = frame.type || frame.rich?.type;
            if (type === 'completion') {
              finish();
            } else if (type === 'error') {
              finish(new Error(frame.data?.message || frame.rich?.data?.message || 'WebSocket request failed'));
            } else {
              controller.enqueue(frame);
            }
          } catch (error) {
            finish(error instanceof Error ? error : new Error('Invalid WebSocket frame'));
          }
        };
        cleanup = () => {
          ws.removeEventListener('message', onMessage);
          ws.removeEventListener('close', onClose);
          ws.removeEventListener('error', onError);
        };
        ws.addEventListener('message', onMessage);
        ws.addEventListener('close', onClose);
        ws.addEventListener('error', onError);
        try {
          ws.send(JSON.stringify({ ...request, request_id: requestId }));
        } catch (error) {
          cleanup();
          throw error;
        }
      },
      cancel() { cleanup(); }
    });

    const reader = stream.getReader();
    return (async function* () {
      try {
        while (true) {
          const { done, value } = await reader.read();
          if (done) return;
          if (value instanceof Error) throw value;
          yield value;
        }
      } finally {
        await reader.cancel();
        reader.releaseLock();
      }
    })();
  }

  /**
   * Send message using polling (fallback option)
   */
  async sendPollMessage(request: ChatRequest): Promise<ChatResponse> {
    const url = this.pollEndpoint.startsWith('http')
      ? this.pollEndpoint
      : `${this.baseUrl}${this.pollEndpoint}`;
    const response = await fetch(url, {
      method: 'POST',
      credentials: 'include',
      headers: {
        'Content-Type': 'application/json',
        ...this.customHeaders,
      },
      body: JSON.stringify(request),
    });

    if (!response.ok) {
      throw new Error(`HTTP ${response.status}: ${response.statusText}`);
    }

    return response.json() as Promise<ChatResponse>;
  }

  /**
   * Generate unique IDs for conversations and requests
   */
  generateId(): string {
    return `${Date.now()}-${Math.random().toString(36).substring(2, 11)}`;
  }
}

/**
 * Default API client instance
 */
export const apiClient = new DoollaApiClient();