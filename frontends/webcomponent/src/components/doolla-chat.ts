import { LitElement, html, css, render } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { doollaDesignTokens } from '../styles/doolla-design-tokens.js';
import { DoollaApiClient, ChatRequest, ChatStreamChunk } from '../services/api-client.js';
import { ComponentManager, ComponentUpdate, RichComponent } from './rich-component-system.js';
import './doolla-status-bar.js';
import './doolla-progress-tracker.js';
import type { DoollaProgressTracker } from './doolla-progress-tracker';
import './rich-card.js';
import './rich-task-list.js';
import './rich-progress-bar.js';
import './plotly-chart.js';

interface AssistantReply {
  content: HTMLElement;
  tracker: DoollaProgressTracker;
}

@customElement('doolla-chat')
export class DoollaChat extends LitElement {
  static styles = [
    doollaDesignTokens,
    css`
      :host {
        /* Shared with messages, query activity, and rich results inside the chat. */
        --chat-primary: #1677ff;
        --chat-primary-hover: #0958d9;
        --chat-accent-soft: #e6f4ff;
        --chat-accent-border: #bae0ff;
        --chat-accent-text: #0958d9;
        --chat-error-text: #a8071a;
        --chat-surface: #fff;
        --chat-muted: #f5f7fa;
        --chat-text: #1f1f1f;
        --chat-text-muted: #595959;
        --chat-outline: #e5e7eb;
        --chat-control-outline: #d9d9d9;
        --chat-focus-ring: 0 0 0 3px rgb(22 119 255 / 16%);
        --chat-gutter: var(--doolla-space-6);
        --chat-content-width: 860px;

        --doolla-background-root: var(--chat-surface);
        --doolla-background-default: var(--chat-surface);
        --doolla-background-higher: var(--chat-muted);
        --doolla-background-highest: var(--chat-muted);
        --doolla-background-subtle: var(--chat-muted);
        --doolla-foreground-default: var(--chat-text);
        --doolla-foreground-dimmer: var(--chat-text-muted);
        --doolla-foreground-dimmest: var(--chat-text-muted);
        --doolla-outline-default: var(--chat-outline);
        --doolla-outline-dimmer: var(--chat-outline);
        --doolla-outline-hover: var(--chat-primary);
        --doolla-accent-primary-default: var(--chat-primary);
        --doolla-accent-primary-hover: var(--chat-primary-hover);
        --doolla-accent-primary-subtle: var(--chat-accent-soft);
        --doolla-border-radius-md: 8px;
        --doolla-border-radius-lg: 8px;
        --doolla-shadow-sm: none;
        --doolla-shadow-md: none;

        display: block;
        max-width: 1120px;
        margin: 0 auto;
        overflow: hidden;
        background: var(--chat-surface);
        border: 1px solid var(--chat-outline);
        border-radius: 12px;
        box-shadow: 0 6px 24px rgb(31 35 41 / 7%);
        color: var(--chat-text);
        font: 14px/1.57 var(--doolla-font-family-default);
      }

      :host([theme="dark"]) {
        color-scheme: dark;
        --chat-primary: #1668dc;
        --chat-primary-hover: #0958d9;
        --chat-accent-soft: #142842;
        --chat-accent-border: #15395b;
        --chat-accent-text: #91caff;
        --chat-error-text: #ffccc7;
        --chat-surface: #141414;
        --chat-muted: #1f1f1f;
        --chat-text: #f5f5f5;
        --chat-text-muted: #bfbfbf;
        --chat-outline: #303030;
        --chat-control-outline: #434343;
      }

      *, *::before, *::after {
        box-sizing: border-box;
      }

      :host(.maximized) {
        position: fixed;
        inset: var(--doolla-space-6);
        max-width: none;
        margin: 0;
        z-index: var(--doolla-z-modal);
      }

      :host(.minimized) {
        position: fixed;
        right: var(--doolla-space-6);
        bottom: var(--doolla-space-6);
        width: 52px;
        height: 52px;
        box-sizing: border-box;
        margin: 0;
        z-index: var(--doolla-z-modal);
        border-radius: 50%;
        border-color: var(--chat-primary);
      }

      :host(.minimized) .chat-layout,
      .minimized-icon {
        display: none;
      }

      :host(.minimized) .minimized-icon {
        display: grid;
        place-items: center;
        width: 100%;
        height: 100%;
        padding: 0;
        border: 0;
        background: var(--chat-primary);
        color: #fff;
        cursor: pointer;
      }

      .minimized-icon:hover {
        background: var(--chat-primary-hover);
      }

      .minimized-icon:focus-visible {
        outline: 2px solid #fff;
        outline-offset: -4px;
        border-radius: 50%;
      }

      .chat-layout {
        display: grid;
        grid-template-columns: minmax(0, 1fr);
        grid-template-rows: auto minmax(0, 1fr);
        height: var(--doolla-chat-height, min(600px, 85vh));
      }

      :host(.maximized) .chat-layout {
        height: 100%;
      }

      .chat-main {
        display: flex;
        flex-direction: column;
        min-width: 0;
        min-height: 0;
      }

      .chat-header {
        display: flex;
        align-items: center;
        gap: var(--doolla-space-3);
        padding: var(--doolla-space-4) var(--chat-gutter);
        border-bottom: 1px solid var(--chat-outline);
      }

      .chat-avatar, .assistant-avatar, .empty-state-icon {
        display: grid;
        place-items: center;
        flex: none;
        border-radius: 8px;
        background: var(--chat-accent-soft);
        color: var(--chat-accent-text);
      }

      .chat-avatar {
        width: 36px;
        height: 36px;
        font-size: 13px;
        font-weight: 600;
      }

      .header-text {
        flex: 1;
        min-width: 0;
        overflow-wrap: anywhere;
      }

      .chat-title {
        margin: 0;
        font-size: 16px;
        font-weight: 600;
      }

      .chat-subtitle, .empty-state-subtitle {
        margin: 0;
        color: var(--chat-text-muted);
      }

      .window-controls {
        display: flex;
        gap: var(--doolla-space-1);
      }

      .window-control-btn, .send-button {
        display: grid;
        place-items: center;
        flex: none;
        padding: 0;
        border: 0;
        border-radius: 6px;
        cursor: pointer;
        transition: background-color var(--doolla-duration-150);
      }

      .window-control-btn {
        width: 36px;
        height: 36px;
        background: transparent;
        color: var(--chat-text-muted);
      }

      .window-control-btn:hover {
        background: var(--chat-muted);
        color: var(--chat-text);
      }

      .window-control-btn svg, .send-button svg {
        width: 18px;
        height: 18px;
      }

      button:focus-visible {
        outline: 2px solid var(--chat-primary);
        outline-offset: 2px;
      }

      .chat-messages {
        flex: 1;
        min-height: 0;
        overflow: auto;
        overscroll-behavior: contain;
        scrollbar-color: var(--chat-control-outline) transparent;
        scrollbar-width: thin;
        scroll-padding-top: var(--chat-gutter);
        padding: var(--chat-gutter);
      }

      .rich-components-container {
        display: flex;
        flex-direction: column;
        gap: var(--doolla-space-3);
      }

      .rich-components-container, .composer {
        width: 100%;
        max-width: var(--chat-content-width);
        margin-inline: auto;
        min-width: 0;
      }

      .assistant-reply {
        display: grid;
        grid-template-columns: 32px minmax(0, 1fr);
        align-items: start;
        gap: var(--doolla-space-2);
      }

      .assistant-reply:not(:has(.assistant-content > *, doolla-progress-tracker[has-activity], doolla-progress-tracker > *)),
      :host(:not([show-progress])) .assistant-reply:not(:has(.assistant-content > *)),
      :host(:not([show-progress])) .assistant-reply doolla-progress-tracker {
        display: none;
      }

      .assistant-avatar {
        width: 32px;
        height: 32px;
        font-size: 11px;
        font-weight: 600;
      }

      .assistant-name {
        display: block;
        margin-bottom: var(--doolla-space-2);
        font-size: 13px;
        font-weight: 600;
      }

      .assistant-content {
        display: flex;
        flex-direction: column;
        gap: var(--doolla-space-3);
        min-width: 0;
      }

      .assistant-reply doolla-progress-tracker {
        width: min(100%, 420px);
      }

      .assistant-reply doolla-progress-tracker:has(> .rich-dataframe, > .rich-text .text-code) {
        width: 100%;
      }

      .assistant-reply doolla-progress-tracker:not(:last-child) {
        margin-bottom: var(--doolla-space-2);
      }

      .assistant-content:empty {
        display: none;
      }

      .assistant-content > .rich-component,
      doolla-progress-tracker > .rich-component {
        min-width: 0;
        margin-bottom: 0;
      }

      doolla-progress-tracker > .rich-component {
        --doolla-background-root: var(--chat-surface);
        --doolla-background-default: var(--chat-surface);
        --doolla-background-higher: var(--chat-muted);
        --doolla-foreground-default: var(--chat-text);
        --doolla-foreground-dimmer: var(--chat-text-muted);
        --doolla-foreground-dimmest: var(--chat-text-muted);
        --doolla-outline-default: var(--chat-outline);
        --doolla-outline-dimmer: var(--chat-outline);
        --doolla-accent-primary-default: var(--chat-primary);
        --doolla-border-radius-md: 8px;
        --doolla-border-radius-lg: 8px;
        --doolla-shadow-sm: none;
        --doolla-shadow-md: none;
      }

      .assistant-content > .rich-text,
      doolla-progress-tracker > .rich-text {
        align-self: flex-start;
        max-width: 100%;
        padding: var(--doolla-space-3) var(--doolla-space-4);
        background: var(--chat-muted);
        border: 0;
        border-radius: 2px 8px 8px;
        overflow-wrap: anywhere;
      }

      .rich-text .text-content {
        white-space: pre-wrap;
      }

      .rich-text .text-markdown {
        padding: 0;
      }

      .rich-text .text-markdown > :first-child {
        margin-top: 0;
      }

      .rich-text .text-markdown > :last-child {
        margin-bottom: 0;
      }

      .rich-text .text-markdown a {
        color: var(--chat-accent-text);
      }

      .rich-text:has(.text-code) .text-title {
        margin: 0 0 var(--doolla-space-2);
        font-size: 13px;
      }

      .rich-text .text-code {
        margin: 0;
        padding: var(--doolla-space-2) var(--doolla-space-3);
        overflow: auto;
        background: var(--chat-surface);
        border: 1px solid var(--chat-outline);
        border-radius: 6px;
      }

      .chat-input-area {
        flex: none;
        padding: var(--doolla-space-3) var(--chat-gutter) var(--doolla-space-4);
        background: var(--chat-surface);
      }

      .chat-input-container {
        display: flex;
        align-items: flex-end;
        gap: var(--doolla-space-2);
        padding: var(--doolla-space-2);
        border: 1px solid var(--chat-control-outline);
        border-radius: 12px;
        background: var(--chat-surface);
        transition: border-color var(--doolla-duration-150), box-shadow var(--doolla-duration-150);
      }

      .chat-input-container:hover,
      .chat-input-container:focus-within {
        border-color: var(--chat-primary);
      }

      .chat-input-container:focus-within {
        box-shadow: var(--chat-focus-ring);
      }

      .message-input {
        flex: 1;
        min-width: 0;
        min-height: 40px;
        max-height: 128px;
        field-sizing: content;
        padding: 9px var(--doolla-space-2);
        border: 0;
        background: transparent;
        color: var(--chat-text);
        font: inherit;
        font-size: 16px;
        resize: none;
        outline: none;
      }

      .message-input::placeholder {
        color: var(--chat-text-muted);
      }

      .message-input:disabled {
        cursor: not-allowed;
      }

      .send-button {
        width: 40px;
        height: 40px;
        background: var(--chat-primary);
        color: #fff;
      }

      .send-button:hover:enabled {
        background: var(--chat-primary-hover);
      }

      .send-button:disabled {
        background: var(--chat-muted);
        color: var(--chat-text-muted);
        cursor: not-allowed;
      }

      .composer-hint {
        margin: var(--doolla-space-2) 0 0;
        color: var(--chat-text-muted);
        font-size: 12px;
      }

      .empty-state {
        display: flex;
        flex-direction: column;
        align-items: center;
        gap: var(--doolla-space-2);
        padding: var(--doolla-space-12) var(--doolla-space-4);
        text-align: center;
      }

      .empty-state[hidden] {
        display: none;
      }

      .empty-state-icon {
        width: 48px;
        height: 48px;
        margin-bottom: var(--doolla-space-1);
      }

      .empty-state-icon svg {
        width: 24px;
        height: 24px;
      }

      .empty-state-text {
        margin: 0;
        font-size: 16px;
        font-weight: 600;
      }

      @media (max-width: 760px) {
        :host {
          --chat-gutter: var(--doolla-space-4);
        }

        :host(.maximized) {
          inset: 0;
          border-radius: 0;
        }
      }

      @media (pointer: coarse) {
        .composer-hint {
          display: none;
        }

        .window-control-btn {
          width: 44px;
          height: 44px;
        }
      }

      @media (prefers-reduced-motion: reduce) {
        *, *::before, *::after {
          transition-duration: 0s !important;
        }
      }
    `
  ];

  @property() title = 'Doolla AI Chat';
  @property() placeholder = 'Ask me anything...';
  @property({ type: Boolean }) disabled = false;
  @property({ type: Boolean, reflect: true, attribute: 'show-progress' }) showProgress = true;
  @property({ type: Boolean }) allowMinimize = true;
  @property({ reflect: true }) theme = 'light';
  @property({ attribute: 'api-base' }) apiBaseUrl = '';
  @property({ attribute: 'sse-endpoint' }) sseEndpoint = '/api/doolla/v2/chat_sse';
  @property({ attribute: 'ws-endpoint' }) wsEndpoint = '/api/doolla/v2/chat_websocket';
  @property({ attribute: 'poll-endpoint' }) pollEndpoint = '/api/doolla/v2/chat_poll';
  @property() subtitle = '';
  @property() startingState: 'normal' | 'maximized' | 'minimized' = 'normal';

  @state() private currentMessage = '';
  @state() private status: 'idle' | 'working' | 'error' | 'success' = 'idle';
  @state() private statusMessage = '';
  @state() private statusDetail = '';
  private _windowState: 'normal' | 'maximized' | 'minimized' = 'normal';

  @property({ reflect: false })
  get windowState() {
    return this._windowState;
  }

  set windowState(value: 'normal' | 'maximized' | 'minimized') {
    console.log('windowState setter called with:', value);
    console.trace('Call stack:');
    const oldValue = this._windowState;
    this._windowState = value;
    this.requestUpdate('windowState', oldValue);
  }

  private apiClient!: DoollaApiClient;
  private conversationId: string;
  private componentManager: ComponentManager | null = null;
  private componentObserver: MutationObserver | null = null;

  constructor() {
    super();
    // Note: Don't create apiClient here - attributes haven't been set yet!
    // It will be created lazily in getApiClient() or firstUpdated()
    this.conversationId = this.generateId();
  }

  /**
   * Ensure API client is created/updated with current endpoint values
   */
  private ensureApiClient() {
    // Always recreate to ensure we have the latest endpoint values
    console.log('[DoollaChat] Creating API client with:', {
      baseUrl: this.apiBaseUrl,
      sseEndpoint: this.sseEndpoint,
      wsEndpoint: this.wsEndpoint,
      pollEndpoint: this.pollEndpoint
    });

    this.apiClient = new DoollaApiClient({
      baseUrl: this.apiBaseUrl,
      sseEndpoint: this.sseEndpoint,
      wsEndpoint: this.wsEndpoint,
      pollEndpoint: this.pollEndpoint
    });
  }

  firstUpdated() {
    // Create API client now that attributes have been set
    this.ensureApiClient();

    // Initialize component manager with rich components container (fallback)
    const richContainer = this.shadowRoot?.querySelector('.rich-components-container') as HTMLElement;
    if (richContainer) {
      this.componentManager = new ComponentManager(richContainer);
      
      // Watch for changes in the rich components container to manage empty state
      this.componentObserver = new MutationObserver(() => {
        // Update empty state visibility
        this.updateEmptyState();
      });
      
      this.componentObserver.observe(richContainer, {
        childList: true,
        subtree: true,
        attributes: true,
        attributeFilter: ['has-activity']
      });
    }

    // Set initial window state from startingState property
    if (this.startingState !== 'normal') {
      this._windowState = this.startingState;
    }

    // Set initial CSS class
    this.classList.add(this._windowState);

    // Request starter UI from backend
    this.requestStarterUI();
  }

  /**
   * Request starter UI (buttons, welcome messages) from backend
   */
  private async requestStarterUI(): Promise<void> {
    try {
      const request = {
        message: "",
        conversation_id: this.conversationId,
        request_id: this.generateId(),
        metadata: {
          starter_ui_request: true
        }
      };

      // Stream the starter UI response
      await this.handleStreamingResponse(request);
    } catch (error) {
      console.error('Error requesting starter UI:', error);
      // Fail silently - starter UI is optional
    }
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    
    // Clean up mutation observer
    if (this.componentObserver) {
      this.componentObserver.disconnect();
      this.componentObserver = null;
    }
  }

  updated(changedProperties: Map<string, any>) {
    super.updated(changedProperties);

    // Update host classes based on window state
    if (changedProperties.has('windowState')) {
      console.log('windowState changed to:', this._windowState);
      this.classList.remove('normal', 'maximized', 'minimized');
      this.classList.add(this._windowState);
      console.log('Applied CSS classes:', this.className);
    }
  }

  private handleInput(e: Event) {
    const input = e.target as HTMLInputElement;
    this.currentMessage = input.value;
  }

  private handleKeyPress(e: KeyboardEvent) {
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      this.sendMessage();
    }
  }

  /**
   * Send a message programmatically (can be called from buttons or external code)
   * Returns a Promise that resolves with success status
   */
  sendMessage(messageText?: string): Promise<boolean> {
    console.log('sendMessage called with:', messageText);

    // Use provided message or fall back to current input
    // Check if messageText is actually a string (not an event object)
    const textToSend = (typeof messageText === 'string') ? messageText : this.currentMessage;

    console.log('Will send:', textToSend);

    if (!textToSend.trim() || this.disabled) {
      console.log('Message empty or disabled, not sending');
      return Promise.resolve(false);
    }

    return this._sendMessageInternal(textToSend);
  }

  private async _sendMessageInternal(messageText: string): Promise<boolean> {
    console.log('_sendMessageInternal called with:', messageText);

    // Auto-maximize window when user sends a message (if not already maximized or minimized)
    if (this.windowState !== 'maximized' && this.windowState !== 'minimized') {
      this.maximizeWindow();
    }

    this.addMessage(messageText, 'user');
    const reply = this.createAssistantReply();
    if (reply) {
      reply.tracker.running = true;
      reply.tracker.activity = '正在发送…';
    }
    this.setStatus(this.showProgress ? 'idle' : 'working', this.showProgress ? '' : 'Sending message...');

    // Clear input only if we're sending from the input field
    if (messageText === this.currentMessage) {
      this.currentMessage = '';
      const input = this.shadowRoot?.querySelector('.message-input') as HTMLTextAreaElement;
      if (input) {
        input.value = '';
      }
    }

    // Dispatch event for external listeners
    this.dispatchEvent(new CustomEvent('message-sent', {
      detail: { message: { content: messageText, type: 'user' } },
      bubbles: true,
      composed: true
    }));

    try {
      // Create the request
      const request = {
        message: messageText,
        conversation_id: this.conversationId,
        request_id: this.generateId(),
        metadata: {}
      };

      // Stream the response
      await this.handleStreamingResponse(request, reply);
      return true; // Success

    } catch (error) {
      console.error('Error sending message:', error);
      this.setStatus('error', 'Failed to send message', error instanceof Error ? error.message : 'Unknown error');

      // Add error message
      reply?.tracker.interrupt();
      this.appendMessage(
        `Sorry, I encountered an error: ${error instanceof Error ? error.message : 'Unknown error'}`,
        'assistant',
        reply?.content
      );
      return false; // Failure
    } finally {
      if (reply) {
        reply.tracker.running = false;
        reply.tracker.activity = '';
      }
    }
  }

  private getTitleInitials(): string {
    const title = (this.title || '').trim();
    if (!title) {
      return 'VA';
    }

    const parts = title.split(/\s+/).filter(Boolean);
    if (parts.length === 1) {
      return parts[0].charAt(0).toUpperCase() || 'V';
    }

    const first = parts[0].charAt(0);
    const last = parts[parts.length - 1].charAt(0);
    const initials = `${first}${last}`.toUpperCase();
    return initials || 'VA';
  }

  private minimizeWindow(e?: Event) {
    if (e) {
      e.stopPropagation();
      e.preventDefault();
    }
    console.log('minimizeWindow called, current state:', this._windowState);
    this.windowState = 'minimized';
    console.log('minimizeWindow set state to:', this._windowState);
    this.dispatchEvent(new CustomEvent('window-state-changed', {
      detail: { state: 'minimized' },
      bubbles: true,
      composed: true
    }));
  }

  private maximizeWindow(e?: Event) {
    if (e) {
      e.stopPropagation();
      e.preventDefault();
    }
    this.windowState = 'maximized';
    this.dispatchEvent(new CustomEvent('window-state-changed', {
      detail: { state: 'maximized' },
      bubbles: true,
      composed: true
    }));
  }

  private restoreWindow(e?: Event) {
    if (e) {
      e.stopPropagation();
      e.preventDefault();
    }
    this.windowState = 'normal';
    this.dispatchEvent(new CustomEvent('window-state-changed', {
      detail: { state: 'normal' },
      bubbles: true,
      composed: true
    }));
  }


  private createAssistantReply(): AssistantReply | undefined {
    const conversation = this.shadowRoot?.querySelector('.rich-components-container');
    if (!conversation) return;

    const row = this.ownerDocument.createElement('section');
    row.className = 'assistant-reply';
    row.setAttribute('aria-label', 'Doolla AI');
    render(html`
      <div class="assistant-avatar" aria-hidden="true">DA</div>
      <div>
        <span class="assistant-name">Doolla AI</span>
        <doolla-progress-tracker collapsible></doolla-progress-tracker>
        <div class="assistant-content"></div>
      </div>
    `, row);
    conversation.append(row);

    return {
      content: row.querySelector<HTMLElement>('.assistant-content')!,
      tracker: row.querySelector<DoollaProgressTracker>('doolla-progress-tracker')!,
    };
  }

  addMessage(content: string, type: 'user' | 'assistant') {
    const reply = type === 'assistant' ? this.createAssistantReply() : undefined;
    this.appendMessage(content, type, reply?.content);
  }

  private appendMessage(content: string, type: 'user' | 'assistant', container?: HTMLElement) {
    const component: RichComponent = {
      id: this.generateId(),
      type: `${type}-message`,
      lifecycle: 'create',
      data: { content, sender: type },
      children: [],
      timestamp: new Date().toISOString(),
      visible: true,
      interactive: false
    };
    this.componentManager?.processUpdate({
      operation: 'create',
      target_id: component.id,
      component,
      timestamp: component.timestamp
    }, { container });
  }

  setStatus(status: typeof this.status, message: string, detail?: string) {
    this.status = status;
    this.statusMessage = message;
    this.statusDetail = detail || '';
  }

  clearStatus() {
    this.statusMessage = '';
    this.statusDetail = '';
    this.status = 'idle';
  }

  getProgressTracker(): DoollaProgressTracker | null {
    if (!this.showProgress) return null;
    const trackers = this.shadowRoot?.querySelectorAll<DoollaProgressTracker>('doolla-progress-tracker');
    return trackers?.[trackers.length - 1] ?? this.createAssistantReply()?.tracker ?? null;
  }

  private async handleStreamingResponse(request: ChatRequest, initialReply?: AssistantReply) {
    if (!this.apiClient || this.apiClient.baseUrl !== this.apiBaseUrl) {
      this.ensureApiClient();
    }

    let reply = initialReply;
    const process = (chunk: ChatStreamChunk) => {
      const update = this.getChunkUpdate(chunk);
      if (!update) return;

      const component = update.component;
      const type = component?.type;
      if (component?.type === 'status_bar_update' && component.data?.status === 'working' && this.showProgress) {
        reply ??= this.createAssistantReply();
        if (reply) {
          reply.tracker.running = true;
          reply.tracker.activity = component.data.message ?? '';
        }
        return;
      }
      if (type === 'status_bar_update' || type === 'chat_input_update' || type === 'user-message') {
        this.componentManager?.processUpdate(update);
        return;
      }

      // Both transports use the row reserved when the request was sent.
      reply ??= this.createAssistantReply();
      const inActivity = component?.data?.chat_section === 'activity' && (this.showProgress || type !== 'dataframe');
      const container = inActivity ? reply?.tracker : reply?.content;
      this.componentManager?.processUpdate(update, { container, taskTracker: reply?.tracker });
    };

    let receivedChunk = false;
    try {
      for await (const chunk of this.apiClient.streamChat(request)) {
        receivedChunk = true;
        process(chunk);
      }
    } catch (error) {
      if (receivedChunk) throw error;
      console.warn('SSE streaming failed, falling back to polling:', error);

      try {
        if (this.showProgress && reply) {
          reply.tracker.activity = '连接中断，正在重试…';
        } else {
          this.setStatus('working', 'Connection issue, retrying...', 'Using fallback method');
        }
        const response = await this.apiClient.sendPollMessage(request);
        for (const chunk of response.chunks) process(chunk);
      } catch (pollError) {
        this.setStatus('error', 'Connection failed', 'Unable to reach server');
        throw pollError;
      }
    } finally {
      // Starter responses can also publish progress from a custom workflow.
      if (!initialReply && reply) {
        reply.tracker.running = false;
        reply.tracker.activity = '';
      }
    }
  }

  private getChunkUpdate(chunk: ChatStreamChunk): ComponentUpdate | undefined {
    this.dispatchEvent(new CustomEvent('chunk-received', {
      detail: { chunk },
      bubbles: true,
      composed: true
    }));
    if (!chunk.rich) return;
    if (chunk.rich.type === 'component_update' && !chunk.rich.lifecycle) {
      return chunk.rich as ComponentUpdate;
    }

    const component = chunk.rich as RichComponent;
    return {
      operation: component.lifecycle || 'create',
      target_id: component.id || this.generateId(),
      component,
      timestamp: component.timestamp || new Date().toISOString()
    };
  }

  private generateId(): string {
    return `${Date.now()}-${Math.random().toString(36).substring(2, 11)}`;
  }

  /**
   * Update the API base URL and recreate the client
   */
  updateApiBaseUrl(baseUrl: string) {
    this.apiBaseUrl = baseUrl;
    this.ensureApiClient();
  }

  /**
   * Get the API client instance for direct access
   */
  getApiClient(): DoollaApiClient {
    if (!this.apiClient) {
      this.ensureApiClient();
    }
    return this.apiClient;
  }

  /**
   * Set custom headers for authentication or other purposes
   */
  setCustomHeaders(headers: Record<string, string>) {
    this.apiClient.setCustomHeaders(headers);
  }

  /**
   * Update empty state visibility based on whether there are components
   */
  private updateEmptyState() {
    const emptyState = this.shadowRoot?.querySelector('#empty-state') as HTMLElement;
    const richContainer = this.shadowRoot?.querySelector('.rich-components-container') as HTMLElement;
    
    if (emptyState && richContainer) {
      // Show empty state if rich container has no children
      emptyState.hidden = !!richContainer.querySelector(
        ':scope > :not(style, .assistant-reply), .assistant-content > *, doolla-progress-tracker[has-activity], doolla-progress-tracker > *'
      );
    }
  }

  /**
   * Scroll to the top of the last message/component that was added
   * This always scrolls regardless of current scroll position
   */
  scrollToLastMessage() {
    const messagesContainer = this.shadowRoot?.querySelector('.chat-messages');
    const richContainer = this.shadowRoot?.querySelector('.rich-components-container');
    
    if (!messagesContainer || !richContainer) return;

    const lastRow = richContainer.lastElementChild;
    if (!lastRow) return;
    const lastComponent = lastRow.querySelector('.assistant-content > :last-child') ?? lastRow;

    // Scroll so the top of the last component is visible
    const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    lastComponent.scrollIntoView({ behavior: reduceMotion ? 'instant' : 'smooth', block: 'start', inline: 'nearest' });
  }

  /**
   * Clear all messages (useful for testing)
   */
  clearMessages() {
    if (this.componentManager) {
      this.componentManager.clear();
    }
    this.updateEmptyState();
    this.requestUpdate();
  }

  /**
   * Add multiple messages at once (useful for testing scrolling)
   */
  addTestMessages(count: number = 10) {
    for (let i = 1; i <= count; i++) {
      setTimeout(() => {
        const type = i % 2 === 0 ? 'assistant' : 'user';
        const content = `This is test message number ${i}. Lorem ipsum dolor sit amet, consectetur adipiscing elit. Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua.`;
        this.addMessage(content, type);
      }, i * 100); // Stagger the messages to simulate real timing
    }
  }

  render() {
    return html`
      <!-- Minimized icon - shown only when minimized via CSS and allowMinimize is true -->
      ${this.allowMinimize ? html`
        <button class="minimized-icon" type="button" aria-label="Open chat" @click=${this.restoreWindow}>
          <svg aria-hidden="true" viewBox="0 0 24 24" fill="currentColor" width="24" height="24">
            <path d="M20 2H4c-1.1 0-2 .9-2 2v12c0 1.1.9 2 2 2h14l4 4V4c0-1.1-.9-2-2-2zm-2 12H6v-2h12v2zm0-3H6V9h12v2zm0-3H6V6h12v2z"/>
          </svg>
        </button>
      ` : ''}

      <!-- Main chat interface -->
      <div class="chat-layout">
        <header class="chat-header">
          <div class="chat-avatar" aria-hidden="true">${this.getTitleInitials()}</div>
          <div class="header-text">
            <h2 class="chat-title">${this.title}</h2>
            ${this.subtitle ? html`<p class="chat-subtitle">${this.subtitle}</p>` : ''}
          </div>
          <div class="window-controls">
            ${this.allowMinimize ? html`
              <button class="window-control-btn minimize" type="button" aria-label="Minimize" title="Minimize"
                @click=${this.minimizeWindow}>
                <svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5">
                  <path d="M5 12h14"/>
                </svg>
              </button>
            ` : ''}
            ${this.windowState === 'maximized' ? html`
              <button class="window-control-btn restore" type="button" aria-label="Restore" title="Restore"
                @click=${this.restoreWindow}>
                <svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5">
                  <path d="M9 5h10v10M5 9h10v10H5z"/>
                </svg>
              </button>
            ` : html`
              <button class="window-control-btn maximize" type="button" aria-label="Maximize" title="Maximize"
                @click=${this.maximizeWindow}>
                <svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5">
                  <path d="M5 5h14v14H5z"/>
                </svg>
              </button>
            `}
          </div>
        </header>
        <div class="chat-main">
          <div class="chat-messages">
            <!-- Empty state - shown when no components exist -->
            <div class="empty-state" id="empty-state">
              <div class="empty-state-icon" aria-hidden="true">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5">
                  <path d="M4 5h16v12H9l-5 3zM8 9h8m-8 4h6" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
              </div>
              <h3 class="empty-state-text">Start a conversation</h3>
              <p class="empty-state-subtitle">Ask a question about your data.</p>
            </div>

            <!-- Rich Components Container - all content renders here via ComponentManager -->
            <div class="rich-components-container" role="log" aria-label="Conversation"></div>
          </div>

          <div class="chat-input-area">
            <div class="composer">
              <doolla-status-bar
                .status=${this.status}
                .message=${this.statusMessage}
                .detail=${this.statusDetail}
                theme=${this.theme}>
              </doolla-status-bar>

              <div class="chat-input-container">
                <textarea
                  class="message-input"
                  aria-label="Message"
                  .placeholder=${this.placeholder}
                  .disabled=${this.disabled}
                  @input=${this.handleInput}
                  @keydown=${this.handleKeyPress}
                  rows="1"
                ></textarea>
                <button
                  class="send-button"
                  type="button"
                  aria-label="Send message"
                  .disabled=${this.disabled || !this.currentMessage.trim()}
                  @click=${this.sendMessage}
                >
                  <svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor"
                    stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
                    <path d="M12 19V5m-6 6 6-6 6 6"/>
                  </svg>
                </button>
              </div>
              <p class="composer-hint">Enter to send · Shift + Enter for a new line</p>
            </div>
          </div>
        </div>

      </div>
    `;
  }
}
