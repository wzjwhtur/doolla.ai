import { css } from 'lit';

// Doolla 2.0 design tokens - Data-First Agents branding
export const doollaDesignTokens = css`
  :host {
    /* Doolla 2.0 Brand Colors */
    --doolla-navy: rgb(2, 61, 96);
    --doolla-cream: rgb(231, 225, 207);
    --doolla-teal: rgb(21, 168, 168);
    --doolla-orange: rgb(254, 93, 38);
    --doolla-magenta: rgb(191, 19, 99);

    /* Color Palette - Light mode (default) */
    --doolla-background-root: rgb(255, 255, 255);
    --doolla-background-default: rgb(231, 225, 207);
    --doolla-background-higher: rgb(244, 246, 248);
    --doolla-background-highest: rgb(229, 231, 235);
    --doolla-background-subtle: rgb(248, 250, 252);
    --doolla-background-lower: rgb(239, 242, 245);

    --doolla-foreground-default: rgb(2, 61, 96);
    --doolla-foreground-dimmer: rgb(71, 85, 105);
    --doolla-foreground-dimmest: rgb(100, 116, 139);

    --doolla-accent-primary-default: rgb(21, 168, 168);
    --doolla-accent-primary-stronger: rgb(2, 61, 96);
    --doolla-accent-primary-strongest: rgb(2, 61, 96);
    --doolla-accent-primary-subtle: rgba(21, 168, 168, 0.1);
    --doolla-accent-primary-hover: rgb(21, 168, 168);

    --doolla-accent-positive-default: rgb(21, 168, 168);
    --doolla-accent-positive-stronger: rgb(2, 61, 96);
    --doolla-accent-positive-subtle: rgba(21, 168, 168, 0.1);

    --doolla-accent-negative-default: rgb(239, 68, 68);
    --doolla-accent-negative-stronger: rgb(220, 38, 38);
    --doolla-accent-negative-subtle: rgba(239, 68, 68, 0.1);

    --doolla-accent-warning-default: rgb(254, 93, 38);
    --doolla-accent-warning-stronger: rgb(254, 93, 38);
    --doolla-accent-warning-subtle: rgba(254, 93, 38, 0.1);

    /* Outline/Border colors */
    --doolla-outline-default: rgba(21, 168, 168, 0.3);
    --doolla-outline-dimmer: rgb(241, 245, 249);
    --doolla-outline-dimmest: rgb(248, 250, 252);
    --doolla-outline-hover: rgb(21, 168, 168);

    /* Typography */
    --doolla-font-family-default: "Space Grotesk", ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, "Noto Sans", sans-serif;
    --doolla-font-family-serif: "Roboto Slab", ui-serif, Georgia, serif;
    --doolla-font-family-mono: "Space Mono", ui-monospace, SFMono-Regular, "SF Mono", Monaco, Inconsolata, "Roboto Mono", "Ubuntu Mono", monospace;

    /* Spacing scale */
    --doolla-space-0: 0px;
    --doolla-space-1: 4px;
    --doolla-space-2: 8px;
    --doolla-space-3: 12px;
    --doolla-space-4: 16px;
    --doolla-space-5: 20px;
    --doolla-space-6: 24px;
    --doolla-space-7: 28px;
    --doolla-space-8: 32px;
    --doolla-space-10: 40px;
    --doolla-space-12: 48px;
    --doolla-space-16: 64px;

    /* Border radius */
    --doolla-border-radius-sm: 6px;
    --doolla-border-radius-md: 10px;
    --doolla-border-radius-lg: 14px;
    --doolla-border-radius-xl: 20px;
    --doolla-border-radius-2xl: 24px;
    --doolla-border-radius-full: 9999px;

    /* Shadows - Preline-inspired */
    --doolla-shadow-xs: 0 1px 2px 0 rgba(0, 0, 0, 0.05);
    --doolla-shadow-sm: 0 1px 3px 0 rgba(0, 0, 0, 0.1), 0 1px 2px -1px rgba(0, 0, 0, 0.1);
    --doolla-shadow-md: 0 4px 6px -1px rgba(0, 0, 0, 0.1), 0 2px 4px -2px rgba(0, 0, 0, 0.1);
    --doolla-shadow-lg: 0 10px 15px -3px rgba(0, 0, 0, 0.1), 0 4px 6px -4px rgba(0, 0, 0, 0.1);
    --doolla-shadow-xl: 0 20px 25px -5px rgba(0, 0, 0, 0.1), 0 8px 10px -6px rgba(0, 0, 0, 0.1);
    --doolla-shadow-2xl: 0 25px 50px -12px rgba(0, 0, 0, 0.25);

    /* Animation durations */
    --doolla-duration-75: 75ms;
    --doolla-duration-100: 100ms;
    --doolla-duration-150: 150ms;
    --doolla-duration-200: 200ms;
    --doolla-duration-300: 300ms;
    --doolla-duration-500: 500ms;
    --doolla-duration-700: 700ms;

    /* Z-index scale */
    --doolla-z-dropdown: 1000;
    --doolla-z-sticky: 1020;
    --doolla-z-fixed: 1030;
    --doolla-z-modal: 1040;
    --doolla-z-popover: 1050;
    --doolla-z-tooltip: 1060;

    /* Chat-specific tokens */
    --doolla-chat-bubble-radius: 18px;
    --doolla-chat-bubble-radius-sm: 12px;
    --doolla-chat-spacing: 16px;
    --doolla-chat-avatar-size: 40px;
  }

  /* Dark theme overrides */
  :host([theme="dark"]) {
    --doolla-background-root: rgb(9, 11, 17);
    --doolla-background-default: rgb(15, 18, 25);
    --doolla-background-higher: rgb(24, 29, 39);
    --doolla-background-highest: rgb(31, 39, 51);
    --doolla-background-subtle: rgb(17, 21, 28);
    --doolla-background-lower: rgb(6, 8, 12);

    --doolla-foreground-default: rgb(248, 250, 252);
    --doolla-foreground-dimmer: rgb(203, 213, 225);
    --doolla-foreground-dimmest: rgb(148, 163, 184);

    --doolla-accent-primary-default: rgb(21, 168, 168);
    --doolla-accent-primary-stronger: rgb(21, 168, 168);
    --doolla-accent-primary-strongest: rgb(2, 61, 96);
    --doolla-accent-primary-subtle: rgba(21, 168, 168, 0.15);
    --doolla-accent-primary-hover: rgb(21, 168, 168);

    --doolla-accent-positive-default: rgb(21, 168, 168);
    --doolla-accent-positive-stronger: rgb(21, 168, 168);
    --doolla-accent-positive-subtle: rgba(21, 168, 168, 0.15);

    --doolla-accent-negative-default: rgb(248, 113, 113);
    --doolla-accent-negative-stronger: rgb(239, 68, 68);
    --doolla-accent-negative-subtle: rgba(248, 113, 113, 0.15);

    --doolla-accent-warning-default: rgb(254, 93, 38);
    --doolla-accent-warning-stronger: rgb(254, 93, 38);
    --doolla-accent-warning-subtle: rgba(254, 93, 38, 0.15);

    --doolla-outline-default: rgba(21, 168, 168, 0.3);
    --doolla-outline-dimmer: rgb(31, 41, 55);
    --doolla-outline-dimmest: rgb(17, 24, 39);
    --doolla-outline-hover: rgb(21, 168, 168);

    --doolla-shadow-xs: 0 1px 2px 0 rgba(0, 0, 0, 0.6);
    --doolla-shadow-sm: 0 1px 3px 0 rgba(0, 0, 0, 0.5), 0 1px 2px -1px rgba(0, 0, 0, 0.5);
    --doolla-shadow-md: 0 4px 6px -1px rgba(0, 0, 0, 0.4), 0 2px 4px -2px rgba(0, 0, 0, 0.4);
    --doolla-shadow-lg: 0 10px 15px -3px rgba(0, 0, 0, 0.4), 0 4px 6px -4px rgba(0, 0, 0, 0.4);
    --doolla-shadow-xl: 0 20px 25px -5px rgba(0, 0, 0, 0.3), 0 8px 10px -6px rgba(0, 0, 0, 0.3);
    --doolla-shadow-2xl: 0 25px 50px -12px rgba(0, 0, 0, 0.6);
  }
`;
