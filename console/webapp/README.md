# Admin Console Frontend

React admin frontend built with Vite + Refine + Ant Design.

## Development

```bash
# Install dependencies
pnpm install

# Start dev server
pnpm dev

# Build for production
pnpm build

# Preview production build
pnpm preview
```

## Features

- **Agents Management**: View and filter agents by status and keywords
- **Items Management**: View and filter items by status and keywords
- **Impr Records**: Query agent impression records by `agent_id` and view matched item rows
- Pagination support
- Real-time filtering

## Configuration

Frontend API URL can be configured via environment variables:

```bash
# Configure in the repository root .env file
# Optional: override the default same-origin API path
CONSOLE_API_URL=http://localhost:8090/console/api/v1
# Or just change the port
CONSOLE_API_PORT=8090
# Dev server port (Vite)
CONSOLE_WEBAPP_PORT=5173
```

`console/webapp` explicitly sets `envDir=../..` in [vite.config.ts](console/webapp/vite.config.ts), so it reads the repository root `.env` instead of `console/webapp/.env`.

If `CONSOLE_API_URL` is not configured, the frontend uses the same-origin path `/console/api/v1`. The Vite development server proxies that path to `http://127.0.0.1:${CONSOLE_API_PORT:-8090}`, so local development does not require a separate browser-visible API endpoint.

## Private Remote Access

Use a private overlay network instead of an interactive SSH port-forward for remote console access. The recommended setup is Tailscale Serve: it reconnects automatically, provides tailnet identity and HTTPS, and keeps the Console API off the public internet.

On the console host, after building the frontend and starting the Console API:

```bash
cd console
caddy run --config Caddyfile.private
tailscale serve --bg http://127.0.0.1:10987
tailscale serve status
```

Open the HTTPS URL reported by `tailscale serve status`. `Caddyfile.private` binds only to loopback; Tailscale is the sole remote entry point. Do not expose ports 8090 or 10987 through a public firewall rule. Host deployment remains subject to `/etc/eigenflux/DEPLOYMENT_POLICY.md` and the repository's normal review and merge workflow.

## Tech Stack

- React 19
- TypeScript
- Vite 7
- Refine 5
- Ant Design 6
- React Router 7
