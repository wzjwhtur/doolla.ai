import { LitElement, html, css } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { doollaDesignTokens } from '../styles/doolla-design-tokens.js';

interface ProgressItem {
  id: string;
  text: string;
  status: 'pending' | 'in_progress' | 'completed' | 'error';
  detail?: string;
}

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

      .thought-content .progress-list {
        background: var(--chat-surface);
      }

      ::slotted(*) {
        margin: var(--doolla-space-2) var(--doolla-space-3);
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
  @property() activity = '';
  @state() private items: ProgressItem[] = [];

  addItem(text: string, detail?: string, id?: string, status: ProgressItem['status'] = 'pending'): string {
    const itemId = id || Date.now().toString();
    const item = { id: itemId, text, status, detail };
    this.items = [...this.items.filter(existing => existing.id !== itemId), item];
    return itemId;
  }

  updateItem(id: string, status?: ProgressItem['status'], detail?: string) {
    this.items = this.items.map(item =>
      item.id === id ? { ...item, status: status ?? item.status, detail: detail ?? item.detail } : item
    );
  }

  removeItem(id: string) {
    this.items = this.items.filter(item => item.id !== id);
  }

  clearItems() {
    this.items = [];
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
    this.items = this.items.map(item => item.status === 'in_progress'
      ? { ...item, status: 'error', detail: '连接中断' }
      : item);
  }

  protected updated() {
    this.syncActivityVisibility();
  }

  private syncActivityVisibility() {
    this.toggleAttribute('has-activity', !!(this.items.length || this.activity || this.children.length));
  }

  private renderItems() {
    return this.items.map(item => html`
      <div class="progress-item ${item.status}" role="listitem">
        <div class="progress-icon ${item.status}" aria-label=${item.status.replace('_', ' ')} role="img">
          ${this.getStatusIcon(item.status)}
        </div>
        <div class="progress-content">
          <p class="progress-text">${item.text}</p>
          ${item.detail ? html`<p class="progress-detail">${item.detail}</p>` : ''}
        </div>
      </div>
    `);
  }

  render() {
    if (this.collapsible) {
      const failures = this.items.filter(item => item.status === 'error').length;
      const state = failures ? 'error' : this.running ? 'running' : '';
      const summary = failures ? `${failures} 个步骤失败`
        : this.running ? '进行中' : this.items.length ? `${this.items.length} 个步骤` : '';
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
          <div class="thought-content">
            ${this.activity ? html`<p class="thought-activity">${this.activity}</p>` : ''}
            ${this.items.length ? html`<div class="progress-list" role="list">${this.renderItems()}</div>` : ''}
            <slot @slotchange=${this.syncActivityVisibility}></slot>
          </div>
        </details>
      `;
    }

    const completed = this.items.filter(item => item.status === 'completed').length;
    return html`
      ${this.items.length > 0 ? html`
        <div class="progress-label">
          <span class="progress-label-text">Tasks</span>
          <span class="progress-summary">${completed}/${this.items.length} completed</span>
        </div>
      ` : ''}

      <div class="progress-list" role="list">
        ${this.items.length === 0
          ? html`<div class="empty-state">No tasks yet</div>`
          : this.renderItems()}
      </div>
    `;
  }
}
