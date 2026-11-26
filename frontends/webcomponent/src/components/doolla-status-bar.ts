import { LitElement, html, css } from 'lit';
import { customElement, property } from 'lit/decorators.js';
import { doollaDesignTokens } from '../styles/doolla-design-tokens.js';

@customElement('doolla-status-bar')
export class DoollaStatusBar extends LitElement {
  static styles = [
    doollaDesignTokens,
    css`
      :host {
        display: block;
        font: 14px/1.55 var(--doolla-font-family-default);
        --status-color: var(--chat-accent-text, #0958d9);
        --status-background: var(--chat-accent-soft, #e6f4ff);
        --status-border: var(--chat-accent-border, #bae0ff);
      }

      :host([status="success"]) {
        --status-color: #237804;
        --status-background: #f6ffed;
        --status-border: #b7eb8f;
      }

      :host([status="warning"]) {
        --status-color: #873800;
        --status-background: #fffbe6;
        --status-border: #ffe58f;
      }

      :host([status="error"]) {
        --status-color: #a8071a;
        --status-background: #fff2f0;
        --status-border: #ffccc7;
      }

      :host([theme="dark"][status="success"]) {
        --status-color: #b7eb8f;
        --status-background: #162312;
        --status-border: #274916;
      }

      :host([theme="dark"][status="warning"]) {
        --status-color: #ffe58f;
        --status-background: #2b2111;
        --status-border: #594214;
      }

      :host([theme="dark"][status="error"]) {
        --status-color: #ffccc7;
        --status-background: #2a1215;
        --status-border: #58181c;
      }

      .status-content {
        display: flex;
        align-items: flex-start;
        gap: var(--doolla-space-2);
        padding: var(--doolla-space-2) var(--doolla-space-3);
        margin: 0 0 var(--doolla-space-2);
        color: var(--status-color);
        background: var(--status-background);
        border: 1px solid var(--status-border);
        border-radius: 8px;
        overflow-wrap: anywhere;
      }

      .status-indicator, .spinner {
        box-sizing: border-box;
        margin-top: 4px;
        flex: none;
        width: 14px;
        height: 14px;
        border-radius: 50%;
      }

      .status-indicator {
        display: grid;
        place-items: center;
        border: 1px solid currentColor;
        font-size: 10px;
        font-weight: 600;
        line-height: 1;
      }

      .spinner {
        border: 2px solid var(--status-border);
        border-top-color: currentColor;
        animation: spin 1s linear infinite;
      }

      .status-text {
        font-weight: 600;
      }

      .status-detail {
        font-size: 12px;
      }

      @keyframes spin {
        to { transform: rotate(360deg); }
      }

      @media (prefers-reduced-motion: reduce) {
        .spinner { animation: none; }
      }
    `
  ];

  @property({ reflect: true }) status: 'idle' | 'working' | 'error' | 'success' | 'warning' = 'idle';
  @property() message = '';
  @property() detail = '';
  @property({ reflect: true }) theme = 'light';

  render() {
    const symbol = this.status === 'success' ? '✓' : this.status === 'idle' ? 'i' : '!';
    return html`
      <div role="status">
        ${this.message.trim() ? html`
          <div class="status-content">
            ${this.status === 'working'
              ? html`<span class="spinner" aria-hidden="true"></span>`
              : html`<span class="status-indicator" aria-hidden="true">${symbol}</span>`}
            <div>
              <div class="status-text">${this.message}</div>
              ${this.detail ? html`<div class="status-detail">${this.detail}</div>` : ''}
            </div>
          </div>
        ` : ''}
      </div>
    `;
  }
}
