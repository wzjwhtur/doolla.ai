import { LitElement, html, css } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { repeat } from 'lit/directives/repeat.js';
import { doollaDesignTokens } from '../styles/doolla-design-tokens.js';

interface ProgressItem {
  kind: 'task';
  id: string;
  text: string;
  status: 'pending' | 'in_progress' | 'completed' | 'error';
  detail?: string;
}

type ActivityEntry = ProgressItem
  | { kind: 'output'; container: HTMLDivElement }
  | { kind: 'status'; text: string };

@customElement('doolla-progress-tracker')
export class DoollaProgressTracker extends LitElement {
  static styles = [
    doollaDesignTokens,
    css`
      :host {
        display: block;
        background: var(--chat-surface, var(--doolla-background-default));
        border: 1px solid var(--chat-outline, var(--doolla-outline-default));
        border-radius: 8px;
        overflow: hidden;
        font-family: var(--doolla-font-family-default);
      }

      :host([collapsible]:not([has-activity])) {
        display: none;
      }

      :host([collapsible]) {
        background: var(--chat-muted);
        overflow: visible;
      }

      .thought-summary {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: var(--doolla-space-2);
        box-sizing: border-box;
        min-height: 40px;
        padding: var(--doolla-space-2) var(--doolla-space-3);
        list-style: none;
        cursor: pointer;
        color: var(--chat-text);
      }

      .thought-summary::-webkit-details-marker {
        display: none;
      }

      .thought-summary:focus-visible {
        outline: 2px solid var(--chat-primary);
        outline-offset: 2px;
        border-radius: 8px;
      }

      .thought-summary svg {
        flex: none;
        width: 14px;
        height: 14px;
        color: var(--chat-text-muted);
        transition: transform var(--doolla-duration-150);
      }

      details[open] .thought-summary svg {
        transform: rotate(90deg);
      }

      .thought-label {
        font-size: 13px;
        font-weight: 600;
      }

      .thought-meta, .thought-activity {
        color: var(--chat-text-muted);
        font-size: 12px;
      }

      .thought-meta.running {
        color: var(--chat-accent-text);
      }

      .thought-meta.error {
        color: var(--chat-error-text, #a8071a);
      }

      .thought-activity {
        margin: 0;
        padding: var(--doolla-space-2) var(--doolla-space-3);
        overflow-wrap: anywhere;
      }

      .thought-content {
        border-top: 1px solid var(--chat-outline);
      }

      .thought-content .progress-item:not(.in_progress, .error) {
        background: var(--chat-surface);
      }

      ::slotted(*) {
        margin: var(--doolla-space-2) var(--doolla-space-3);
      }

      ::slotted(.thought-output) {
        display: flex;
        flex-direction: column;
        gap: var(--doolla-space-2);
        min-width: 0;
      }

      .progress-label {
        padding: var(--doolla-space-3) var(--doolla-space-4) var(--doolla-space-2);
        display: flex;
        align-items: center;
        justify-content: space-between;
      }

      .progress-label-text {
        font-size: 11px;
        font-weight: 500;
        color: var(--chat-text-muted, var(--doolla-foreground-dimmest));
        margin: 0;
      }

      .progress-summary {
        font-size: 10px;
        color: var(--chat-text-muted, var(--doolla-foreground-dimmest));
        font-weight: 400;
      }

      .progress-list {
        max-height: 300px;
        overflow-y: auto;
        padding-top: 0;
      }

      .progress-item {
        padding: var(--doolla-space-3) var(--doolla-space-4);
        border-bottom: 1px solid var(--doolla-outline-dimmest);
        display: flex;
        align-items: flex-start;
        gap: var(--doolla-space-3);
        transition: background var(--doolla-duration-150) ease;
      }

      .progress-item:last-child {
        border-bottom: none;
      }

      .progress-item:hover {
        background: var(--chat-muted, var(--doolla-background-higher));
      }

      .progress-item.in_progress {
        background: var(--chat-accent-soft, var(--doolla-accent-primary-subtle));
        border-left: 3px solid var(--chat-primary, var(--doolla-accent-primary-default));
        padding-left: calc(var(--doolla-space-4) - 3px);
      }

      .progress-item.completed {
        opacity: 0.7;
      }

      .progress-item.error {
        background: var(--doolla-accent-negative-subtle);
        border-left: 3px solid var(--doolla-accent-negative-default);
        padding-left: calc(var(--doolla-space-4) - 3px);
      }

      .progress-item.error .progress-text {
        color: var(--chat-error-text, var(--doolla-accent-negative-stronger));
      }

      .progress-item.error .progress-detail {
        color: var(--chat-error-text, var(--doolla-accent-negative-default));
        font-weight: 500;
      }

      .progress-icon {
        width: 16px;
        height: 16px;
        border-radius: 50%;
        display: flex;
        align-items: center;
        justify-content: center;
        flex-shrink: 0;
        margin-top: 1px;
      }

      .progress-icon.pending {
        background: var(--doolla-outline-default);
      }

      .progress-icon.in_progress {
        background: var(--chat-primary, var(--doolla-accent-primary-default));
      }

      .progress-icon.completed {
        background: #389e0d;
      }

      .progress-icon.error {
        background: var(--doolla-accent-negative-default);
        box-shadow: 0 0 0 2px var(--doolla-accent-negative-subtle);
      }

      .progress-icon svg {
        width: 10px;
        height: 10px;
        color: white;
      }

      .progress-icon.error svg {
        width: 8px;
        height: 8px;
        color: white;
      }

      .spinner-mini {
        width: 10px;
        height: 10px;
        border: 1.5px solid rgba(255, 255, 255, 0.3);
        border-top-color: white;
        border-radius: 50%;
        animation: spin 1s linear infinite;
      }

      .progress-content {
        flex: 1;
        min-width: 0;
        overflow-wrap: anywhere;
      }

      .progress-text {
        font-size: 13px;
        color: var(--chat-text, var(--doolla-foreground-default));
        font-weight: 500;
        margin: 0 0 var(--doolla-space-1) 0;
        line-height: 1.3;
      }

      .progress-detail {
        font-size: 11px;
        color: var(--chat-text-muted, var(--doolla-foreground-dimmest));
        margin: 0;
        line-height: 1.3;
      }

      .empty-state {
        padding: var(--doolla-space-6) var(--doolla-space-4);
        color: var(--chat-text-muted, var(--doolla-foreground-dimmest));
        font-size: 12px;
      }

      @media (prefers-reduced-motion: reduce) {
        .spinner-mini { animation: none; }
        .thought-summary svg { transition: none; }
      }

      @keyframes spin {
        to {
          transform: rotate(360deg);
        }
      }
    `
  ];

  @property() title = 'Progress';
  @property() theme = 'light';
  @property({ type: Boolean, reflect: true }) collapsible = false;
  @property({ type: Boolean }) running = false;
  @state() private entries: ActivityEntry[] = [];
  private nextId = 0;
  private readonly outputObserver = new MutationObserver(() => this.syncActivityVisibility());

  connectedCallback() {
    super.connectedCallback();
    this.outputObserver.observe(this, { childList: true });
    for (const entry of this.entries) {
      if (entry.kind === 'output') this.outputObserver.observe(entry.container, { childList: true });
    }
  }

  disconnectedCallback() {
    this.outputObserver.disconnect();
    super.disconnectedCallback();
  }

  @property()
  get activity(): string {
    return this.entries.find(entry => entry.kind === 'status')?.text ?? '';
  }

  set activity(text: string) {
    const entries = this.entries.filter(entry => entry.kind !== 'status');
    this.entries = text ? [...entries, { kind: 'status', text }] : entries;
  }

  // Keep rich components in light DOM so their renderers retain state and handlers.
  // Named slots place each run of output where it arrived among the task events.
  getActivityContainer(): HTMLElement {
    const last = this.entries[this.entries.length - 1];
    if (last?.kind === 'output') return last.container;

    const container = this.ownerDocument.createElement('div');
    container.className = 'thought-output';
    container.setAttribute('role', 'listitem');
    container.slot = `activity-${++this.nextId}`;
    this.append(container);
    if (this.isConnected) this.outputObserver.observe(container, { childList: true });
    this.entries = [...this.entries, { kind: 'output', container }];
    return container;
  }

  addItem(text: string, detail?: string, id?: string, status: ProgressItem['status'] = 'pending'): string {
    const itemId = id || `task-${++this.nextId}`;
    const item: ProgressItem = { kind: 'task', id: itemId, text, status, detail };
    const index = this.entries.findIndex(entry => entry.kind === 'task' && entry.id === itemId);
    if (index === -1) {
      this.entries = [...this.entries, item];
    } else {
      const next = [...this.entries];
      next[index] = item;
      this.entries = next;
    }
    return itemId;
  }

  updateItem(id: string, status?: ProgressItem['status'], detail?: string) {
    this.entries = this.entries.map(entry =>
      entry.kind === 'task' && entry.id === id
        ? { ...entry, status: status ?? entry.status, detail: detail ?? entry.detail }
        : entry
    );
  }

  removeItem(id: string) {
    this.entries = this.entries.filter(entry => entry.kind !== 'task' || entry.id !== id);
  }

  clearItems() {
    this.entries = this.entries.filter(entry => entry.kind !== 'task');
  }

  private getStatusIcon(status: ProgressItem['status']) {
    switch (status) {
      case 'pending':
        return html``;
      case 'in_progress':
        return html`<div class="spinner-mini"></div>`;
      case 'completed':
        return html`
          <svg viewBox="0 0 20 20" fill="currentColor">
            <path fill-rule="evenodd" d="M16.707 5.293a1 1 0 010 1.414l-8 8a1 1 0 01-1.414 0l-4-4a1 1 0 011.414-1.414L8 12.586l7.293-7.293a1 1 0 011.414 0z" clip-rule="evenodd" />
          </svg>
        `;
      case 'error':
        return html`
          <svg viewBox="0 0 20 20" fill="currentColor">
            <path fill-rule="evenodd" d="M10 18a8 8 0 100-16 8 8 0 000 16zM8.707 7.293a1 1 0 00-1.414 1.414L8.586 10l-1.293 1.293a1 1 0 101.414 1.414L10 11.414l1.293 1.293a1 1 0 001.414-1.414L11.414 10l1.293-1.293a1 1 0 00-1.414-1.414L10 8.586 8.707 7.293z" clip-rule="evenodd" />
          </svg>
        `;
    }
  }

  interrupt() {
    this.entries = this.entries.map(entry => entry.kind === 'task' && entry.status === 'in_progress'
      ? { ...entry, status: 'error', detail: '连接中断' }
      : entry);
  }

  protected updated() {
    this.syncActivityVisibility();
  }

  private syncActivityVisibility() {
    const hasActivity = this.entries.some(entry => entry.kind !== 'output' || entry.container.childElementCount);
    const hasDirectOutput = Array.from(this.children).some(child => !child.slot);
    this.toggleAttribute('has-activity', hasActivity || hasDirectOutput);
  }

  private renderItem(item: ProgressItem) {
    return html`
      <div class="progress-item ${item.status}" role="listitem">
        <div class="progress-icon ${item.status}" aria-label=${item.status.replace('_', ' ')} role="img">
          ${this.getStatusIcon(item.status)}
        </div>
        <div class="progress-content">
          <p class="progress-text">${item.text}</p>
          ${item.detail ? html`<p class="progress-detail">${item.detail}</p>` : ''}
        </div>
      </div>
    `;
  }

  private renderActivity(entry: ActivityEntry) {
    switch (entry.kind) {
      case 'task':
        return this.renderItem(entry);
      case 'output':
        return html`<slot name=${entry.container.slot}></slot>`;
      case 'status':
        return html`<p class="thought-activity" role="listitem">${entry.text}</p>`;
    }
  }

  render() {
    const items = this.entries.filter(entry => entry.kind === 'task');
    if (this.collapsible) {
      const failures = items.filter(item => item.status === 'error').length;
      const state = failures ? 'error' : this.running ? 'running' : '';
      const summary = failures ? `${failures} 个步骤失败`
        : this.running ? '进行中' : items.length ? `${items.length} 个步骤` : '';
      return html`
        <details>
          <summary class="thought-summary">
            <svg aria-hidden="true" viewBox="0 0 16 16" fill="none" stroke="currentColor"
              stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
              <path d="m6 4 4 4-4 4"/>
            </svg>
            <span class="thought-label">思考过程</span>
            <span class="thought-meta ${state}">${summary}</span>
          </summary>
          <div class="thought-content" role="list">
            ${repeat(
              this.entries,
              entry => entry.kind === 'task' ? `task:${entry.id}` : entry.kind === 'output' ? entry.container : 'status',
              entry => this.renderActivity(entry)
            )}
            <slot></slot>
          </div>
        </details>
      `;
    }

    const completed = items.filter(item => item.status === 'completed').length;
    return html`
      ${items.length > 0 ? html`
        <div class="progress-label">
          <span class="progress-label-text">Tasks</span>
          <span class="progress-summary">${completed}/${items.length} completed</span>
        </div>
      ` : ''}

      <div class="progress-list" role="list">
        ${items.length === 0
          ? html`<div class="empty-state">No tasks yet</div>`
          : items.map(item => this.renderItem(item))}
      </div>
    `;
  }
}
