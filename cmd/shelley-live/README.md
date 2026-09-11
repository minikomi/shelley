# Shelley Live experiment

A small browser voice interface that uses OpenAI Realtime over WebRTC and starts
repository-aware planning or implementation work in Shelley.

## Run

```bash
go build -o bin/shelley-live ./cmd/shelley-live
./bin/shelley-live
```

The server listens on port `8765` by default.

## Configuration

- `OPENAI_BASE_URL`: token-mint API base. Defaults to the exe.dev OpenAI
  integration at `https://openai.int.exe.xyz`.
- `OPENAI_API_KEY`: optional when using the exe.dev integration; required when
  `OPENAI_BASE_URL=https://api.openai.com`.
- `SHELLEY_URL`: Shelley HTTP URL or Unix socket URL. Defaults to
  `unix:///home/exedev/.config/shelley/shelley.sock`.
- `SHELLEY_LIVE_CWD`: working directory for new tasks. Defaults to this Shelley
  checkout.
- `SHELLEY_LIVE_ROOT`: allowed root for per-session working-directory changes.
  Defaults to `/home/exedev`.
- `SHELLEY_PUBLIC_URL`: browser-visible Shelley base URL.
- `SHELLEY_USER_EMAIL`: author attached to proxied user messages.
- `LISTEN_ADDR`: defaults to `:8765`.

The browser receives only a short-lived Realtime client secret. The durable
OpenAI credential remains behind the server or exe.dev integration proxy.
