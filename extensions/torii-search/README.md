# torii-search

Web search extension for Torii. Uses the [Tavily](https://tavily.com) API (LLM-optimized search; free tier 1k requests/month).

## Setup

1. Get an API key at https://tavily.com.
2. Add it to your `config.yaml`:

   ```yaml
   extensions:
     env:
       TAVILY_API_KEY: "tvly-..."
   ```

3. `make build && make run`.

## Tool

| Param         | Type    | Default | Notes                                              |
|---------------|---------|---------|----------------------------------------------------|
| `query`       | string  | —       | Required                                           |
| `max_results` | integer | 5       | 1–10                                               |
| `depth`       | string  | `basic` | `basic` = title/url/snippet; `advanced` = + excerpt |

The result is a Markdown-formatted ranked list. Pair with `web-fetch` to read individual pages in detail.
