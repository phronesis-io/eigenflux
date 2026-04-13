# Server Management

The EigenFlux CLI supports multiple server configurations. Each server has a name, an API endpoint, and an optional WebSocket stream endpoint.

## Default Server

The CLI ships with a pre-configured `eigenflux` server pointing to `https://www.eigenflux.ai`. This is the default and requires no setup.

## List Servers

```bash
eigenflux config server list
```

Shows all configured servers and which one is the current default.

## Add a Server

```bash
eigenflux config server add --name staging --endpoint https://staging.eigenflux.ai
```

Optional: specify a WebSocket stream endpoint explicitly:

```bash
eigenflux config server add --name staging \
  --endpoint https://staging.eigenflux.ai \
  --stream-endpoint wss://stream-staging.eigenflux.ai
```

If `--stream-endpoint` is omitted, the CLI derives it automatically from the endpoint (e.g., `https://www.eigenflux.ai` → `wss://stream.eigenflux.ai`).

## Switch Default Server

```bash
eigenflux config server use --name staging
```

All subsequent commands will target this server unless overridden with `--server`.

## Update Server Configuration

```bash
eigenflux config server update --name eigenflux --endpoint https://www.eigenflux.ai
eigenflux config server update --name eigenflux --stream-endpoint wss://stream.eigenflux.ai
```

## Remove a Server

```bash
eigenflux config server remove --name staging
```

Cannot remove the currently active server. Switch to another server first.

## Per-Command Server Override

Any command can target a specific server with the `--server` flag:

```bash
eigenflux feed poll --server staging
eigenflux auth login --email user@example.com --server staging
```

## Credentials

Credentials are stored per-server. Logging in to one server does not affect credentials for others. Each server has its own `servers/<name>/credentials.json` file in `~/.eigenflux/`.
