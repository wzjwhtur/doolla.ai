# Getting started with Doolla

[简体中文使用指南](../使用指南.md)

The chat interface, API documentation, and Go CLI support English and Simplified Chinese.

## Set the service language

Start with `-locale en` or `-locale zh-CN`. The same setting is available as `locale` in a YAML or JSON config file. An explicit flag takes precedence over the config file and `DOOLLA_LOCALE`. The default is English.

```sh
go -C src/doolla run ./cmd/doolla -locale en -h
```

Open your service in a browser and choose **English** or **简体中文** in the chat header. The built-in page remembers your choice. Switching languages updates the controls immediately and applies to the next message. Previous messages keep their original wording.

## Ask questions and plot data

Ask a question about a configured database, or enter `/help` to see the available commands. Expand **Activity** to inspect executed SQL and query results. The result table can be searched and exported as CSV.

To create a chart, ask Doolla to query the data and plot the result in the same message. When a turn has one result, the chart tool can use it directly. When there are multiple results, the tool selects one using its `result_id`. An existing CSV can also be selected explicitly by filename.

## Embed or call the API

```html
<script type="module" src="/assets/doolla-components.js"></script>
<doolla-chat locale="en"></doolla-chat>
```

SSE, polling, and WebSocket requests accept the same optional `locale` field. When no supported body locale is supplied, the server checks `Accept-Language` before using its default.

```sh
curl -fsS http://127.0.0.1:8000/api/doolla/v2/chat_poll \
  -H 'Content-Type: application/json' \
  -d '{"message":"/help","locale":"en"}'
```

Open `/docs?locale=en` or `/docs?locale=zh-CN` on the running service to explore the API. Keep the same request ID and locale when retrying the same turn.

Read the final `turn_outcome` chunk to distinguish `succeeded`, `partial`, and `failed`. SSE `[DONE]` and WebSocket `completion` mark the end of the transport. In an embedded page, listen for `turn-completed` or check the Boolean returned by `sendMessage()`.

For Go SDK requests, call `doolla.WithLocale(ctx, "en")` before `Agent.Chat`. Custom hooks and prompt builders can read the selected language with `doolla.CurrentLocale(ctx)`.

## Maintain translations

Edit `src/doolla/internal/i18n/catalog/en.json` and `zh-CN.json` together. The Go binary embeds both catalogs. The frontend build generates its own bundle from the shared web entries. Keep translation keys and placeholders aligned, then run the Go and browser suites.
