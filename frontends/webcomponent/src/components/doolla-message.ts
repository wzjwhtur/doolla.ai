import { LitElement, html, css } from 'lit';
import { customElement, property } from 'lit/decorators.js';
import { doollaDesignTokens } from '../styles/doolla-design-tokens.js';

@customElement('doolla-message')
export class DoollaMessage extends LitElement {
  static styles = [
    doollaDesignTokens,
    css`
      :host {
        display: block;
        font: 14px/1.65 var(--doolla-font-family-default);
      }

      :host([theme="dark"]) {
        --message-default-surface: #1f1f1f;
        --message-default-user-surface: #142842;
        --message-default-text: #f5f5f5;
        --message-default-meta: #bfbfbf;
      }

      .message {
        width: fit-content;
        max-width: 85%;
        padding: var(--doolla-space-2) var(--doolla-space-4);
        background: var(--chat-muted, var(--message-default-surface, #f5f7fa));
        color: var(--chat-text, var(--message-default-text, #1f1f1f));
        border-radius: 2px 8px 8px;
        overflow-wrap: anywhere;
      }

      .message.user {
        margin-inline-start: auto;
        background: var(--chat-accent-soft, var(--message-default-user-surface, #e6f4ff));
        border-radius: 8px 2px 8px 8px;
      }

      .message-sender {
        margin-bottom: var(--doolla-space-2);
        color: var(--chat-text, var(--message-default-text, #1f1f1f));
        font-size: 13px;
        font-weight: 600;
        text-align: end;
      }

      .message-content {
        white-space: pre-wrap;
      }

      .message-timestamp {
        margin-top: var(--doolla-space-1);
        color: var(--chat-text-muted, var(--message-default-meta, #595959));
        font-size: 11px;
        font-variant-numeric: tabular-nums;
      }

      .message.user .message-timestamp {
        text-align: end;
      }
    `
  ];

  @property() content = '';
  @property() type: 'user' | 'assistant' = 'user';
  @property({ type: Number }) timestamp = Date.now();
  @property({ reflect: true }) theme = 'light';

  private formatTimestamp(timestamp: number): string {
    return new Date(timestamp).toLocaleTimeString([], {
      hour: '2-digit',
      minute: '2-digit'
    });
  }

  render() {
    return html`
      ${this.type === 'user' ? html`<div class="message-sender" lang="zh">我</div>` : ''}
      <div class="message ${this.type}">
        <div class="message-content">${this.content}</div>
        <div class="message-timestamp">
          ${this.formatTimestamp(this.timestamp)}
        </div>
      </div>
    `;
  }
}
