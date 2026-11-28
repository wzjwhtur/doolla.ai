import { DoollaApiClient } from '../src/services/api-client';
import { version as plotlyVersion } from 'plotly.js-dist-min/package.json';
import type { RichCard } from '../src/components/rich-card';
import type { DoollaProgressTracker } from '../src/components/doolla-progress-tracker';
import { DoollaChat } from '../src/components/doolla-chat';
import { ArtifactComponentRenderer, ComponentManager, DataFrameComponentRenderer, NotificationComponentRenderer } from '../src/components/rich-component-system';

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

async function testHeaderLifecycle() {
  const requests: { url: string; headers: Headers; redirect?: string }[] = [];
  window.fetch = async (url, init) => {
    requests.push({ url: String(url), headers: new Headers(init?.headers), redirect: init?.redirect });
    return String(url).includes('poll') ? Response.json({ chunks: [] }) : new Response('data: [DONE]\n\n');
  };
  const chat = new DoollaChat();
  chat.apiBaseUrl = 'https://first.example';
  chat.setCustomHeaders({ Authorization: 'Bearer original' });
  document.body.append(chat);
  try {
    await chat.updateComplete;
    check(requests[0].headers.get('authorization') === 'Bearer original', 'authenticated starter before firstUpdated');
    chat.updateApiBaseUrl('https://first.example/enterprise');
    await chat.getApiClient().sendPollMessage(request);
    check(requests[1].headers.get('authorization') === 'Bearer original', 'same-origin recreation preserves credentials');
    chat.customHeaders = { Authorization: 'Bearer refreshed', 'X-Account': 'one' };
    await chat.sendMessage('refresh');
    check(requests[2].headers.get('authorization') === 'Bearer refreshed' && requests[2].redirect === 'error', 'token refresh and guarded redirects');
    chat.pollEndpoint = 'https://other.example/poll';
    await chat.getApiClient().sendPollMessage(request);
    check(!requests[3].headers.has('authorization') && !requests[3].headers.has('x-account'), 'absolute endpoint must not receive credentials');
    chat.apiBaseUrl = 'https://other.example';
    await chat.sendMessage('switch');
    check(!requests[4].headers.has('authorization') && !requests[4].headers.has('x-account'), 'new API origin requires a fresh header binding');
    chat.setAttribute('custom-headers', '{"X-Account":"two"}');
    await chat.getApiClient().sendPollMessage(request);
    check(requests[5].headers.get('x-account') === 'two', 'declarative header refresh');
    chat.removeAttribute('custom-headers');
    await chat.getApiClient().sendPollMessage(request);
    check(!requests[6].headers.has('x-account'), 'removing headers clears credentials');

    for (const attrs of [
      `custom-headers='{"X-Account":"markup"}' api-base="https://first.example"`,
      `api-base="https://first.example" custom-headers='{"X-Account":"markup"}'`,
    ]) {
      const template = document.createElement('template');
      template.innerHTML = `<doolla-chat ${attrs}></doolla-chat>`;
      const fromMarkup = template.content.firstElementChild as DoollaChat;
      document.body.append(fromMarkup);
      try {
        await fromMarkup.updateComplete;
        check(requests[requests.length - 1].headers.get('x-account') === 'markup', 'markup configuration is independent of attribute order');
      } finally { fromMarkup.remove(); }
    }

    const base = document.createElement('base');
    base.href = 'https://first.example/';
    document.head.append(base);
    try {
      chat.apiBaseUrl = '/analytics';
      chat.pollEndpoint = '/poll';
      chat.setCustomHeaders({ Authorization: 'Bearer scoped' });
      const bound = chat.getApiClient();
      base.href = 'https://other.example/';
      await bound.sendPollMessage(request);
      check(!requests[requests.length - 1].headers.has('authorization'), 'old client refuses credentials after document base moves');
      await chat.getApiClient().sendPollMessage(request);
      check(!requests[requests.length - 1].headers.has('authorization'), 'new client discards previous origin credentials');
    } finally { base.remove(); }
  } finally {
    chat.remove();
  }
}

async function testVisibleAnswerDeltas() {
  const chat = new DoollaChat();
  let controller!: ReadableStreamDefaultController<Uint8Array>;
  window.fetch = async (_url, init) => {
    const payload = JSON.parse(String(init?.body));
    if (payload.metadata?.starter_ui_request) return new Response('data: [DONE]\n\n');
    check(payload.metadata?.answer_deltas === true, 'opt in to previews');
    return new Response(new ReadableStream({ start(c) { controller = c; } }));
  };
  const chunks = (type: string, data: Record<string, any>) => {
    const seen = new Promise<void>(resolve => chat.addEventListener('chunk-received', () => resolve(), { once: true }));
    controller.enqueue(new TextEncoder().encode('data: ' + JSON.stringify({ ...frame(type), rich: { type, id: type, data } }) + '\n\n'));
    return seen;
  };
  document.body.append(chat);
  try {
    await chat.updateComplete;
    const done = chat.sendMessage('stream');
    await chunks('answer_delta', { text: '<i>provisional</i>' });
    await chunks('answer_delta', { text: ' again' });
    check(chat.shadowRoot!.textContent?.includes('<i>provisional</i> again') && !chat.shadowRoot!.querySelector('i'), 'plain preview arrives before stream completion');
    await chunks('sql_statement', { content: 'SELECT 1', chat_section: 'activity' });
    check(!chat.shadowRoot!.textContent?.includes('provisional'), 'tool call clears preview');
    await chunks('answer_delta', { text: 'finalizing' });
    await chunks('text', { content: '**complete**', markdown: true });
    controller.enqueue(new TextEncoder().encode('data: [DONE]\n\n'));
    controller.close();
    check(await done && chat.shadowRoot!.querySelector('strong')?.textContent === 'complete' &&
      !chat.shadowRoot!.textContent?.includes('finalizing'), 'one final Markdown answer replaces preview');
    const failed = chat.sendMessage('interrupt');
    await chunks('answer_delta', { text: 'unfinished preview' });
    controller.close();
    check(!(await failed) && !chat.shadowRoot!.textContent?.includes('unfinished preview'), 'failed stream removes provisional text');
  } finally { chat.remove(); }
}

async function testScopedConfirmationAndExport() {
  const first = new DoollaChat();
  const second = new DoollaChat();
  window.fetch = async () => new Response('data: [DONE]\n\n');
  document.body.append(first, second);
  try {
    await Promise.all([first.updateComplete, second.updateComplete]);
    const sent: string[] = [];
    first.sendMessage = async () => { sent.push('wrong widget'); return true; };
    second.sendMessage = async command => { sent.push(command!); return true; };
    const card = document.createElement('rich-card') as RichCard;
    card.content = 'SELECT 1';
    card.actions = [{ action: '/confirm-sql test-id', label: 'Confirm' }];
    second.shadowRoot!.append(card);
    await card.updateComplete;
    card.shadowRoot!.querySelector<HTMLButtonElement>('button.card-action')!.click();
    check(sent.join('') === '/confirm-sql test-id', 'confirmation stays with its chat');
    const renderer = new ArtifactComponentRenderer();
    const root = renderer.render({ id: 'chart', lifecycle: 'create', type: 'artifact', children: [],
      timestamp: '', visible: true, interactive: true,
      data: { artifact_type: 'html', dependencies: ['plotly'], external_renderable: true,
        content: '<div id="chart"></div>', title: 'Export' } });
    let page = '';
    root.addEventListener('artifact-opened', event => {
      const detail = (event as CustomEvent).detail;
      page = detail.getStandaloneHTML();
      detail.preventDefault();
    });
    root.querySelector<HTMLButtonElement>('.external-btn')!.click();
    const html = new DOMParser().parseFromString(page, 'text/html');
    check(html.querySelector('script')?.src === 'https://cdn.plot.ly/plotly-' + plotlyVersion + '.min.js' &&
      !!html.querySelector('#chart'), 'standalone export uses the installed Plotly version');
  } finally { first.remove(); second.remove(); }
}

async function testTurnOutcome() {
  for (const [status, poll] of [['succeeded', false], ['failed', false], ['partial', true]] as const) {
    const code = status === 'failed' ? 'processing_error' : status === 'partial' ? 'query_partial' : undefined;
    const terminal = { ...frame('terminal'), rich: { type: 'turn_outcome', id: 'terminal', data: { status, code } } };
    window.fetch = async (url, init) => {
      if (JSON.parse(String(init?.body)).metadata?.starter_ui_request) return new Response('data: [DONE]\n\n');
      const chunks = [frame('Completed work'), terminal];
      if (String(url).endsWith('chat_poll')) return Response.json({ chunks });
      if (poll) return new Response('', { status: 503 });
      return new Response(chunks.map(chunk => 'data: ' + JSON.stringify(chunk) + '\n\n').join('') + 'data: [DONE]\n\n');
    };
    const chat = new DoollaChat();
    document.body.append(chat);
    try {
      await chat.updateComplete;
      const outcomes: CustomEvent[] = [];
      chat.addEventListener('turn-completed', event => outcomes.push(event as CustomEvent));
      check(await chat.sendMessage('question') === (status === 'succeeded'), 'business outcome return value');
      check(outcomes.length === 1 && outcomes[0].detail.status === status && outcomes[0].detail.code === code,
        'business outcome event after the response ends');
      check(chat.shadowRoot!.querySelector('[data-component-id="Completed work"]') &&
        !chat.shadowRoot!.querySelector('[data-component-id="terminal"]'), 'result stays visible and terminal metadata does not render');
    } finally {
      chat.remove();
    }
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

async function testComponentLifecycle() {
  const output = document.createElement('div');
  document.body.append(output);
  const manager = new ComponentManager(output);
  const timestamp = new Date().toISOString();
  const create = (id: string, type: string, data: Record<string, any>) => manager.processUpdate({
    operation: 'create', target_id: id, timestamp,
    component: { id, type, data, timestamp, lifecycle: 'create', visible: true, interactive: false, children: [] },
  });
  const update = (id: string, updates: Record<string, any>) => manager.processUpdate({ operation: 'update', target_id: id, timestamp, updates });
  const remove = (id: string) => manager.processUpdate({ operation: 'remove', target_id: id, timestamp });
  try {
    create('steps', 'card', { title: 'Steps', content: '**First**', markdown: true, collapsible: true });
    create('query', 'text', { content: 'SELECT 1' });
    const card = output.querySelector('rich-card') as HTMLElement & { updateComplete: Promise<unknown> };
    await card.updateComplete;
    card.shadowRoot!.querySelector<HTMLButtonElement>('.card-toggle')!.click();
    for (const content of ['Second', 'Third']) {
      update('steps', { content: `**${content}**` });
      await card.updateComplete;
      check(card.shadowRoot?.querySelector('.card-content.collapsed strong')?.textContent === content &&
        card.nextElementSibling?.getAttribute('data-component-id') === 'query', 'update keeps the rich card, Markdown, collapse and execution order');
    }
    remove('steps');
    check(!output.querySelector('rich-card') && !manager.getComponent('steps'), 'remove targets the current card');
    update('query', { content: 'SELECT 2' });
    update('query', { content: 'SELECT 3' });
    check(output.querySelector('[data-component-id="query"]')?.textContent?.trim() === 'SELECT 3', 're-rendered text receives subsequent updates');
    remove('query');
    check(!output.querySelector('[data-component-id="query"]'), 'remove targets the replacement node');

    create('result', 'dataframe', { columns: ['item', 'count'], data: [{ item: 'A', count: 1 }, { item: 'AA', count: 3 }, { item: 'B', count: 2 }] });
    const original = output.querySelector<HTMLElement>('[data-component-id="result"]')!;
    const count = original.querySelector<HTMLElement>('[data-column="count"]')!;
    count.click(); count.click();
    const filter = original.querySelector<HTMLInputElement>('input')!;
    filter.value = 'a'; filter.dispatchEvent(new Event('input'));
    update('result', { data: [{ item: 'AB', count: 5 }, { item: 'AC', count: 6 }, { item: 'B', count: 8 }] });
    const next = output.querySelector<HTMLElement>('[data-component-id="result"]')!;
    check(next !== original && next.querySelector<HTMLInputElement>('input')?.value === 'a' &&
      next.querySelector<HTMLElement>('[data-column="count"]')?.dataset.sortDirection === 'desc' &&
      Array.from(next.querySelectorAll('tbody td:first-child'), cell => cell.textContent).join(',') === 'AC,AB', 'table replacement restores filter and sort');
  } finally {
    output.remove();
  }
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
  chat.locale = 'zh-CN';
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
    send(0, 'text', 'next-step', { content: 'check the next source', chat_section: 'activity' });
    task(0, { operation: 'add_task', task: { id: 'follow-up', title: 'follow-up SQL', status: 'in_progress' } });
    send(0, 'sql_statement', 'follow-up-sql', { content: 'SELECT 2', code_language: 'sql', chat_section: 'activity' });
    task(0, { operation: 'update_task', task_id: 'follow-up', status: 'completed' });
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
    const timeline = Array.from(details[0].querySelector('.thought-content')!.children)
      .flatMap(node => node instanceof HTMLSlotElement
        ? node.assignedElements().flatMap(group => Array.from(group.children))
        : [node])
      .map(node => node.getAttribute('data-component-id') ?? node.querySelector('.progress-text')?.textContent);
    check(timeline.join(',') === 'first SQL,sql-activity,query-rows,next-step,follow-up SQL,follow-up-sql',
      'tool steps and rich output follow execution order even when earlier steps finish later');

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

async function testLocaleSwitchPreservesConversation() {
  const requested: { locale: string; message: string }[] = [];
  window.fetch = async (_url, init) => {
    const request = JSON.parse(String(init?.body));
    requested.push(request);
    const rich = request.message === 'show rows'
      ? { type: 'dataframe', id: 'rows', data: { chat_section: 'activity', title: '查询结果', row_count: 2, column_count: 1, columns: ['label'], data: [{ label: 'apple' }, { label: 'pear' }] } }
      : { type: 'text', id: request.message || 'welcome', data: { content: request.locale === 'en' ? 'Hello' : '你好' } };
    return new Response('data: ' + JSON.stringify({ rich: { ...rich, lifecycle: 'create' } }) + '\n\ndata: [DONE]\n\n');
  };
  const chat = new DoollaChat();
  chat.locale = 'zh-CN';
  document.body.append(chat);
  try {
    await chat.updateComplete;
    check(await chat.sendMessage('show rows'), 'query in Chinese locale');
    const root = chat.shadowRoot!;
    const result = root.querySelector('[data-component-id="rows"]')!;
    const search = result.querySelector('input')!;
    check(search.placeholder === '搜索…', 'initial table language');
    search.value = 'apple';
    search.dispatchEvent(new Event('input'));
    const tracker = chat.getProgressTracker()!;
    await tracker.updateComplete;
    const panel = tracker.shadowRoot!.querySelector('details')!;
    panel.open = true;
    const draft = root.querySelector('textarea')!;
    draft.value = 'draft';
    draft.dispatchEvent(new Event('input'));
    const select = root.querySelector('select')!;
    select.value = 'en';
    select.dispatchEvent(new Event('change'));
    await chat.updateComplete;
    check(chat.lang === 'en' && search.getAttribute('placeholder') === 'Search...', 'existing table labels switch to English');
    check(panel.open && draft.value === 'draft' && search.value === 'apple' &&
      result.querySelectorAll('tbody tr').length === 1 && result.querySelector('h3')?.textContent === '查询结果',
      'switch preserves draft, expanded state, filtered rows and previous result text');
    check(requested.length === 2 && requested.every(r => r.locale === 'zh-CN'), 'switch did not issue a chat request');
    check(await chat.sendMessage('/help') && requested[requested.length - 1]?.locale === 'en', 'next turn uses selected locale');
  } finally {
    chat.remove();
  }
}

(async () => {
  try {
    await testHeaderLifecycle(); await testVisibleAnswerDeltas(); await testScopedConfirmationAndExport();
    await testSse(); await testTurnOutcome(); await testWebSocket(); testDataframe(); testToolErrorNotification(); await testTaskTracker();
    await testFinalAnswerMarkdown(); await testComponentLifecycle(); await testAssistantReplies(); await testLocaleSwitchPreservesConversation();
    document.body.dataset.testResult = 'passed';
  } catch (error) {
    document.body.textContent = String(error);
    document.body.dataset.testResult = 'failed';
  } finally {
    window.fetch = originalFetch;
  }
})();
