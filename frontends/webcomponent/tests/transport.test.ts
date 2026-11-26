import { DoollaApiClient } from '../src/services/api-client';
import type { DoollaProgressTracker } from '../src/components/doolla-progress-tracker';
import { DoollaChat } from '../src/components/doolla-chat';
import { ComponentManager, DataFrameComponentRenderer, NotificationComponentRenderer } from '../src/components/rich-component-system';

function check(value: unknown, message: string): asserts value {
  if (!value) throw new Error(message);
}
async function rejects(promise: Promise<unknown>, pattern: RegExp) {
  try { await promise; } catch (error) {
    check(pattern.test(String(error)), 'Unexpected failure: ' + error);
    return;
  }
  throw new Error('Expected rejection: ' + pattern);
}
const client = new DoollaApiClient();
const request = { message: 'question', request_id: 'request' };
const frame = (text: string) => ({ rich: { type: 'text', id: text, data: { content: text } },
  conversation_id: 'conversation', request_id: 'request', timestamp: 0 });
const originalFetch = window.fetch;

async function testSse() {
  const bytes = new TextEncoder().encode('data:' + JSON.stringify(frame('回答')) + '\r\n\r\ndata: [DONE]\r\n\r\n');
  window.fetch = async () => new Response(new ReadableStream({
    start(controller) { for (const byte of bytes) controller.enqueue(Uint8Array.of(byte)); controller.close(); }
  }));
  const chunks = [];
  for await (const c of client.streamChat(request)) chunks.push(c);
  check(chunks.length === 1 && chunks[0].rich.data.content === '回答', 'split SSE frame');

  for (const partial of [false, true]) {
    let polls = 0;
    window.fetch = async (url, init) => {
      if (JSON.parse(String(init?.body)).metadata?.starter_ui_request) return new Response('data: [DONE]\n\n');
      if (String(url).endsWith('chat_poll')) {
        polls++;
        return Response.json({ chunks: [frame('recovered')] });
      }
      return new Response(partial ? 'data: ' + JSON.stringify(frame('partial')) + '\n\n' : '');
    };
    const chat = new DoollaChat();
    document.body.append(chat);
    await chat.updateComplete;
    const ok = await chat.sendMessage('question');
    chat.remove();
    check(ok === !partial && polls === (partial ? 0 : 1), 'SSE premature EOF / Poll replay');
  }
}

class Socket extends EventTarget {
  readyState = WebSocket.OPEN;
  send(_data: string) {}
  frame(data: unknown) { this.dispatchEvent(new MessageEvent('message', { data: JSON.stringify(data) })); }
}

async function testFinalAnswerMarkdown() {
  // Match the padded, variable-length code fences emitted by scalarColumn in Go.
  const scalar = '查询结果（` total_orders `）为 42。\n\n查询结果（`` a`b ``）为 3。';
  const report = [
    '## 生产日报', '**工单**统计', '- 完工', '1. 复核', '[详情](https://example.com/report)',
    '| 工单 | 数量 |\n| --- | --- |\n| A | 42 |', '```sql\nSELECT 1 < 2\n```',
    '<span data-report-markup>数据库标签</span>', '[本地文件](file:///report)',
  ].join('\n\n');
  const plain = '**原文** <em>标签</em>';
  const components = [
    { type: 'text', id: 'scalar', data: { content: scalar, markdown: true } },
    { type: 'text', id: 'report', data: { content: report, markdown: true } },
    { type: 'text', id: 'plain', data: { content: plain, markdown: false } },
    { type: 'card', id: 'workflow', data: { title: '核对', content: plain, markdown: true } },
  ];
  window.fetch = async (_url, init) => {
    const { metadata, request_id, conversation_id } = JSON.parse(String(init?.body));
    const events = metadata?.starter_ui_request ? [] : components.map(component => {
      const rich = { ...component, lifecycle: 'create' };
      return 'data: ' + JSON.stringify({ rich, request_id, conversation_id });
    });
    return new Response([...events, 'data: [DONE]', ''].join('\n\n'));
  };
  const chat = new DoollaChat();
  document.body.append(chat);
  try {
    await chat.updateComplete;
    check(await chat.sendMessage('显示生产日报'), 'final answer SSE response');
    const tracker = chat.getProgressTracker()!;
    await tracker.updateComplete;
    check(getComputedStyle(tracker).display === 'none', 'a response without tool activity has no empty thought panel');
    const answer = chat.shadowRoot!.querySelector('[data-component-id="scalar"]');
    const reportNode = chat.shadowRoot!.querySelector('[data-component-id="report"]');
    const plainNode = chat.shadowRoot!.querySelector('[data-component-id="plain"]');
    check(answer && reportNode && plainNode, 'final answer components');
    const aliases = Array.from(answer.querySelectorAll('p code'), code => code.textContent);
    check(aliases.length === 2 && aliases[0] === 'total_orders' && aliases[1] === 'a`b', 'scalar column inline code, padding, and embedded backtick');
    check(reportNode.querySelector('h2')?.textContent === '生产日报', 'Markdown heading');
    check(reportNode.querySelector('strong')?.textContent === '工单', 'Markdown emphasis');
    check(reportNode.querySelector('ul > li')?.textContent === '完工', 'Markdown bullet list');
    check(reportNode.querySelector('ol > li')?.textContent === '复核', 'Markdown ordered list');
    check(reportNode.querySelector('a[href="https://example.com/report"]')?.textContent === '详情', 'Markdown report link');
    check(reportNode.querySelector('table th')?.textContent?.trim() === '工单' &&
      reportNode.querySelector('table td:last-child')?.textContent?.trim() === '42', 'Markdown result table');
    check(reportNode.querySelector('pre > code')?.textContent?.trimEnd() === 'SELECT 1 < 2', 'fenced SQL');
    check(!reportNode.querySelector('[data-report-markup]'), 'raw HTML in Markdown must stay inert');
    check(!reportNode.querySelector('a[href^="file:"]'), 'local file link must stay inert');
    check(plainNode.textContent?.trim() === plain && !plainNode.querySelector('strong, em'), 'literal text when Markdown is disabled');
    const card = chat.shadowRoot!.querySelector('rich-card')?.shadowRoot;
    check(card?.querySelector('strong')?.textContent === '原文' && !card.querySelector('em'),
      'workflow card renders Markdown while keeping literal HTML inert');
  } finally {
    chat.remove();
  }
}
async function testWebSocket() {
  const socket = new Socket();
  const send = () => client.sendWebSocketMessage(socket as unknown as WebSocket, request);
  socket.send = () => {
    socket.frame({ ...frame('other'), request_id: 'another' });
    socket.frame(frame('first'));
    socket.frame(frame('second'));
    socket.frame({ type: 'completion', request_id: 'request' });
  };
  const chunks = [];
  for await (const chunk of await send()) chunks.push(chunk.rich.id);
  check(chunks.join(',') === 'first,second', 'early/buffered WebSocket frames');

  socket.send = () => {};
  const iterator = await send();
  const next = iterator.next();
  socket.frame(frame('waiting'));
  check((await next).value?.rich.id === 'waiting', 'pending WebSocket next');
  socket.frame(frame('buffered'));
  socket.dispatchEvent(new Event('close'));
  check((await iterator.next()).value?.rich.id === 'buffered', 'queued frames before close');
  await rejects(iterator.next(), /closed before completion/);

  const failed = await send();
  socket.frame({ type: 'error', data: { message: 'provider failed' } });
  await rejects(failed.next(), /provider failed/);
  const disconnected = await send();
  const waiting = disconnected.next();
  socket.dispatchEvent(new Event('error'));
  await rejects(waiting, /connection error/);
}

function testToolErrorNotification() {
  const message = 'Tool error: invalid column <sample>';
  const rendered = new NotificationComponentRenderer().render({
    id: 'tool-error', type: 'notification', lifecycle: 'create', visible: true, children: [],
    interactive: false, timestamp: '', data: { level: 'error', message },
  });
  check(rendered.querySelector('.notification-message')?.textContent === message, 'literal tool error text');
}

function testDataframe() {
  const name = 'A & "B" <unit>';
  const markup = '<em>units</em>';
  const table = new DataFrameComponentRenderer().render({
    id: 'result', type: 'dataframe', lifecycle: 'create', visible: true, children: [],
    interactive: true, timestamp: '', data: { title: markup, description: markup,
      columns: [name], data: [{ [name]: markup }, { [name]: '0' }], column_types: { [name]: 'number' } },
  });
  const head = table.querySelector('th')!;
  check(head.dataset.column === name && head.textContent?.includes(name), 'column attribute and label');
  check(!table.querySelector('em') && table.querySelector('td')?.textContent === markup, 'dataframe text');
  head.click();
  check(head.dataset.sortDirection === 'asc', 'sort quoted column');
  const firstSorted = table.querySelector('td')?.textContent;
  head.click();
  check(table.querySelector('td')?.textContent !== firstSorted, 'reverse sorting');
  const search = table.querySelector('input')!;
  search.value = 'units'; search.dispatchEvent(new Event('input'));
  check(table.querySelectorAll('tbody tr').length === 1 && table.querySelector('td')?.textContent === markup, 'filter text');
}

async function testTaskTracker() {
  const host = document.createElement('div');
  document.body.append(host);
  const shadow = host.attachShadow({ mode: 'open' });
  const tracker = document.createElement('doolla-progress-tracker') as DoollaProgressTracker;
  const output = document.createElement('div');
  shadow.append(tracker, output);
  const manager = new ComponentManager(output);
  async function send(data: Record<string, unknown>) {
    const id = 'doolla-task-tracker';
    manager.processUpdate({ operation: 'create', target_id: id, timestamp: '', component: {
      id, type: 'task_tracker_update', visible: true, interactive: false, children: [], lifecycle: 'create', timestamp: '', data,
    } });
    await tracker.updateComplete;
  }
  await send({ operation: 'add_task', task: { id: 'sql', title: 'Execute run_sql', status: 'in_progress' } });
  check(tracker.shadowRoot?.querySelector('.progress-icon.in_progress'), 'tool in progress');
  await send({ operation: 'update_task', task_id: 'sql', status: 'completed' });
  check(tracker.shadowRoot?.querySelector('.progress-icon.completed'), 'tool completed');
  await send({ operation: 'remove_task', task_id: 'sql' });
  check(!tracker.shadowRoot?.querySelector('.progress-item'), 'tool removed');
  host.remove();
}

async function testAssistantReplies() {
  const streams = new Map<string, ReadableStreamDefaultController<Uint8Array>>();
  const encoder = new TextEncoder();
  window.fetch = async (_url, init) => {
    const { message, metadata } = JSON.parse(String(init?.body));
    if (metadata?.starter_ui_request) return new Response('data: [DONE]\n\n');
    return new Response(new ReadableStream<Uint8Array>({
      start(controller) { streams.set(message, controller); },
    }));
  };

  const chat = new DoollaChat();
  document.body.append(chat);
  try {
    await chat.updateComplete;
    const first = chat.sendMessage('first question');
    const second = chat.sendMessage('second question');
    const openStreams = ['first question', 'second question'].map(question => streams.get(question)!);
    const send = (turn: number, type: string, id: string, data: Record<string, unknown>) => {
      const chunk = { rich: { type, id, lifecycle: 'create', data } };
      openStreams[turn].enqueue(encoder.encode('data: ' + JSON.stringify(chunk) + '\n\n'));
    };
    const task = (turn: number, data: Record<string, unknown>) =>
      send(turn, 'task_tracker_update', 'doolla-task-tracker', data);
    task(0, { operation: 'add_task', task: { id: 'run', title: 'first SQL', status: 'in_progress' } });
    task(1, { operation: 'add_task', task: { id: 'run', title: 'second SQL', status: 'in_progress' } });
    send(1, 'text', 'step', { content: 'checking schema', chat_section: 'activity' });
    task(1, { operation: 'update_task', task_id: 'run', status: 'error' });
    send(1, 'text', 'second-answer', { content: 'second answer' });
    const query = "SELECT '<span data-sql-html>' AS sample";
    send(0, 'sql_statement', 'sql-activity', { title: '生成的 SQL', content: query, code_language: 'sql', chat_section: 'activity' });
    send(0, 'dataframe', 'query-rows', { title: 'Query Results', columns: ['sample'], data: [{ sample: 'value' }], chat_section: 'activity' });
    task(0, { operation: 'update_task', task_id: 'run', status: 'completed' });
    send(0, 'text', 'first-answer', { content: 'first answer' });
    for (const stream of openStreams) {
      stream.enqueue(encoder.encode('data: [DONE]\n\n'));
      stream.close();
    }
    check(await first && await second, 'both requests finish');

    const replies = Array.from(chat.shadowRoot!.querySelectorAll('.assistant-reply'));
    const trackers = replies.map(row => row.querySelector<DoollaProgressTracker>('doolla-progress-tracker')!);
    await Promise.all(trackers.map(tracker => tracker.updateComplete));
    check(replies.length === 2 && replies.every(row => row.querySelector('.assistant-name')?.textContent === 'Doolla AI'),
      'assistant identity is visible in each reply');
    check(replies[0].querySelector('.assistant-content')?.textContent === 'first answer' &&
      replies[1].querySelector('.assistant-content')?.textContent === 'second answer',
      'out-of-order responses stay with their own request');
    check(replies[1].querySelector('doolla-progress-tracker [data-component-id="step"]'),
      'intermediate rich content goes inside the thought panel');
    check(trackers[0].querySelector('.text-code > code.language-sql')?.textContent === query &&
      !trackers[0].querySelector('[data-sql-html]'), 'generated SQL renders as literal code inside the thought panel');
    check(trackers[0].querySelector('.rich-dataframe tbody td')?.textContent === 'value' &&
      !replies[0].querySelector('.assistant-content .rich-dataframe'), 'query results render inside the thought panel');
    const details = trackers.map(tracker => tracker.shadowRoot!.querySelector('details')!);
    check(details.every(detail => !detail.open), 'thought panels are collapsed by default');
    check(details[0].querySelector('.progress-item.completed')?.textContent?.includes('first SQL') &&
      details[1].querySelector('.progress-item.error')?.textContent?.includes('second SQL'),
      'each thought panel keeps its own tool state');
    check(details[1].querySelector('summary')?.textContent?.includes('1 个步骤失败'),
      'failed steps are visible in the collapsed summary');
    details[1].querySelector('summary')!.click();
    check(details[1].open && !details[0].open, 'opening one thought panel leaves other replies closed');
  } finally {
    chat.remove();
  }
}

(async () => {
  try {
    await testSse(); await testWebSocket(); testDataframe(); testToolErrorNotification(); await testTaskTracker();
    await testFinalAnswerMarkdown(); await testAssistantReplies();
    document.body.dataset.testResult = 'passed';
  } catch (error) {
    document.body.textContent = String(error);
    document.body.dataset.testResult = 'failed';
  } finally {
    window.fetch = originalFetch;
  }
})();
