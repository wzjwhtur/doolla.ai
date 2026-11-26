import MarkdownIt from 'markdown-it';

// Agent responses may contain database values. Treat raw HTML as text and
// keep markdown-it's built-in link validation enabled.
const markdown = new MarkdownIt({ html: false });

export function renderMarkdown(content: string): string {
  return markdown.render(content);
}
