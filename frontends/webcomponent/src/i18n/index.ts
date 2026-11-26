import { catalogs } from './catalog.generated.js';

export type Locale = keyof typeof catalogs;
export type MessageKey = keyof typeof catalogs.en;

export function normalizeLocale(value: string = ''): Locale | undefined {
  try {
    const tag = new Intl.Locale(value.replace(/_/g, '-')).maximize();
    if (tag.language === 'en') return 'en';
    if (tag.language === 'zh' && tag.script === 'Hans') return 'zh-CN';
  } catch {
    // Malformed or unsupported preferences leave selection to the next source.
  }
  return undefined;
}

export function browserLocale(): Locale | undefined {
  if (typeof navigator === 'undefined') return undefined;
  for (const preferred of navigator.languages ?? [navigator.language]) {
    const locale = normalizeLocale(preferred);
    if (locale) return locale;
  }
  return undefined;
}

export function t(locale: Locale, key: MessageKey, ...args: (string | number)[]): string {
  const message = catalogs[locale][key] ?? catalogs.en[key];
  if (!message) return key;
  return message.replace(/\{(\d+)\}/g, (marker, index) => {
    const value = args[Number(index)];
    return typeof value === 'number' ? new Intl.NumberFormat(locale).format(value) : value ?? marker;
  });
}

// Update inherited labels in imperative rich components without replacing
// inputs, tool timelines, or other DOM owned by their renderers.
export function localizeDOM(root: ParentNode, locale: Locale): void {
  for (const element of root.querySelectorAll<HTMLElement>('[data-i18n], [data-i18n-placeholder], [data-i18n-title], [data-i18n-label]')) {
    const { i18n, i18nPlaceholder, i18nTitle, i18nLabel, i18nArgs } = element.dataset;
    let args: (string | number)[] = [];
    if (i18nArgs) {
      try {
        const values: unknown = JSON.parse(i18nArgs);
        if (Array.isArray(values)) args = values;
      } catch { /* Malformed label arguments leave the template intact. */ }
    }
    if (i18n) element.textContent = t(locale, i18n as MessageKey, ...args);
    if (i18nPlaceholder) element.setAttribute('placeholder', t(locale, i18nPlaceholder as MessageKey));
    if (i18nTitle) element.title = t(locale, i18nTitle as MessageKey);
    if (i18nLabel) element.setAttribute('aria-label', t(locale, i18nLabel as MessageKey));
  }
}

export function setWidgetLocales(root: ParentNode, locale: Locale): void {
  const selector = 'doolla-message, doolla-progress-tracker, rich-task-list, plotly-chart';
  const widgets = Array.from(root.querySelectorAll<HTMLElement & { locale: Locale }>(selector));
  if (root instanceof HTMLElement && root.matches(selector)) {
    widgets.unshift(root as HTMLElement & { locale: Locale });
  }
  for (const widget of widgets) widget.locale = locale;
}
