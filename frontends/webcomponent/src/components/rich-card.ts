import { LitElement, html, css } from 'lit';
import { customElement, property } from 'lit/decorators.js';
import { doollaDesignTokens } from '../styles/doolla-design-tokens.js';
import { renderMarkdown } from './markdown.js';

export interface CardAction {
  label: string;
  action: string;
  variant?: 'primary' | 'secondary';
}

@customElement('rich-card')
export class RichCard extends LitElement {
  static styles = [
    doollaDesignTokens,
    css`
      :host {
        display: block;
        margin-bottom: var(--doolla-space-4);
        font-family: var(--doolla-font-family-default);
      }

      .card {
        border: 1px solid var(--doolla-outline-default);
        border-radius: var(--doolla-border-radius-lg);
        background: var(--doolla-background-default);
        box-shadow: var(--doolla-shadow-sm);
        overflow: hidden;
        transition: box-shadow var(--doolla-duration-200) ease;
      }

      .card:hover {
        box-shadow: var(--doolla-shadow-md);
      }

      .card-header {
        display: flex;
        align-items: center;
        padding: var(--doolla-space-4) var(--doolla-space-5);
        background: var(--doolla-background-higher);
        border-bottom: 1px solid var(--doolla-outline-default);
        gap: var(--doolla-space-3);
      }

      .card-header.collapsible {
        cursor: pointer;
      }

      .card-icon {
        font-size: 1.25rem;
        display: flex;
        align-items: center;
      }

      .card-title-section {
        flex: 1;
      }

      .card-title {
        margin: 0;
        font-size: 1rem;
        font-weight: 600;
        color: var(--doolla-foreground-default);
      }

      .card-subtitle {
        margin: var(--doolla-space-1) 0 0 0;
        font-size: 0.875rem;
        color: var(--doolla-foreground-dimmer);
      }

      .card-status {
        padding: var(--doolla-space-1) var(--doolla-space-2);
        border-radius: var(--doolla-border-radius-md);
        font-size: 0.75rem;
        font-weight: 600;
        text-transform: uppercase;
      }

      .card-status.status-success {
        background: #d4edda;
        color: #155724;
      }

      .card-status.status-warning {
        background: #fff3cd;
        color: #856404;
      }

      .card-status.status-error {
        background: #f8d7da;
        color: #721c24;
      }

      .card-status.status-info {
        background: #d1ecf1;
        color: #0c5460;
      }

      .card-toggle {
        background: none;
        border: none;
        cursor: pointer;
        font-size: 1rem;
        color: var(--doolla-foreground-dimmer);
        padding: var(--doolla-space-1);
        border-radius: var(--doolla-border-radius-sm);
        transition: background-color var(--doolla-duration-200) ease;
      }

      .card-toggle:hover {
        background: var(--doolla-background-root);
      }

      .card-content {
        padding: var(--doolla-space-4) var(--doolla-space-5);
        line-height: 1.5;
        color: var(--doolla-foreground-default);
        transition: all var(--doolla-duration-200) ease;
        overflow: hidden;
      }

      .card-content.collapsed {
        max-height: 0;
        padding-top: 0;
        padding-bottom: 0;
      }

      .card-content h1,
      .card-content h2,
      .card-content h3 {
        margin: var(--doolla-space-2) 0;
        font-weight: 600;
      }

      .card-content h1 {
        font-size: 1.5rem;
      }

      .card-content h2 {
        font-size: 1.25rem;
      }

      .card-content h3 {
        font-size: 1.125rem;
      }

      .card-content p {
        margin: var(--doolla-space-2) 0;
      }

      .card-content ul {
        margin: var(--doolla-space-2) 0;
        padding-left: var(--doolla-space-5);
      }

      .card-content li {
        margin: var(--doolla-space-1) 0;
      }

      .card-content code {
        background: var(--doolla-background-higher);
        padding: var(--doolla-space-1) var(--doolla-space-2);
        border-radius: var(--doolla-border-radius-sm);
        font-family: monospace;
        font-size: 0.875em;
      }

      .card-content strong {
        font-weight: 600;
      }

      .card-actions {
        padding: var(--doolla-space-3) var(--doolla-space-5);
        background: var(--doolla-background-root);
        border-top: 1px solid var(--doolla-outline-default);
        display: flex;
        gap: var(--doolla-space-2);
      }

      .card-action {
        padding: var(--doolla-space-2) var(--doolla-space-4);
        border-radius: var(--doolla-border-radius-md);
        border: 1px solid var(--doolla-outline-default);
        background: var(--doolla-background-default);
        color: var(--doolla-foreground-default);
        cursor: pointer;
        font-size: 0.875rem;
        font-weight: 500;
        transition: all var(--doolla-duration-200) ease;
      }

      .card-action:hover {
        background: var(--doolla-background-higher);
      }

      .card-action.primary {
        background: var(--doolla-accent-primary-default);
        color: white;
        border-color: var(--doolla-accent-primary-default);
      }

      .card-action.primary:hover {
        background: var(--doolla-accent-primary-stronger);
      }
    `
  ];

  @property() title = '';
  @property() subtitle = '';
  @property() content = '';
  @property() icon = '';
  @property() status: 'info' | 'success' | 'warning' | 'error' = 'info';
  @property({ type: Array }) actions: CardAction[] = [];
  @property({ type: Boolean }) collapsible = false;
  @property({ type: Boolean }) collapsed = false;
  @property({ type: Boolean }) markdown = false;
  @property() theme: 'light' | 'dark' = 'dark';

  private _toggleCollapsed() {
    if (this.collapsible) {
      this.collapsed = !this.collapsed;
    }
  }

  render() {
    const contentHtml = this.markdown
      ? html`<div class="card-content ${this.collapsed ? 'collapsed' : ''}" .innerHTML=${renderMarkdown(this.content)}></div>`
      : html`<div class="card-content ${this.collapsed ? 'collapsed' : ''}">${this.content}</div>`;

    return html`
      <div class="card">
        <div class="card-header ${this.collapsible ? 'collapsible' : ''}"
             @click=${this._toggleCollapsed}>
          ${this.icon ? html`<span class="card-icon">${this.icon}</span>` : ''}
          <div class="card-title-section">
            <h3 class="card-title">${this.title}</h3>
            ${this.subtitle ? html`<p class="card-subtitle">${this.subtitle}</p>` : ''}
          </div>
          ${this.status ? html`<span class="card-status status-${this.status}">${this.status}</span>` : ''}
          ${this.collapsible ? html`
            <button class="card-toggle">${this.collapsed ? '▶' : '▼'}</button>
          ` : ''}
        </div>
        ${contentHtml}
        ${this.actions.length > 0 ? html`
          <div class="card-actions">
            ${this.actions.map(action => html`
              <button class="card-action ${action.variant || 'secondary'}"
                      @click=${() => this._handleAction(action.action)}>
                ${action.label}
              </button>
            `)}
          </div>
        ` : ''}
      </div>
    `;
  }

  private async _handleAction(action: string) {
    console.log('🔘 Card action button clicked (rich-card)');
    console.log('   Action:', action);

    // Dispatch event for any listeners
    this.dispatchEvent(new CustomEvent('card-action', {
      detail: { action },
      bubbles: true,
      composed: true
    }));

    // Also directly send to doolla-chat
    const doollaChat = document.querySelector('doolla-chat') as any;
    if (doollaChat && typeof doollaChat.sendMessage === 'function') {
      console.log('   Found doolla-chat, sending message...');
      try {
        const success = await doollaChat.sendMessage(action);
        if (success) {
          console.log('   ✅ Action sent successfully');
        } else {
          console.error('   ❌ Failed to send action');
        }
      } catch (error) {
        console.error('   ❌ Error sending action:', error);
      }
    } else {
      console.warn('   ⚠️ doolla-chat component not found or sendMessage not available');
    }
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'rich-card': RichCard;
  }
}
