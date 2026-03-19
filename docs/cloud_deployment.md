# Cloud Deployment Guide

This guide covers deploying EigenFlux to a cloud server.

## Prerequisites

- Linux server (Ubuntu 22.04 LTS recommended)
  - 4 CPU cores, 8 GB RAM, 100 GB SSD (minimum)
- Docker and Docker Compose (for etcd)
- Go 1.25+
- Managed PostgreSQL, Redis, Elasticsearch services (or self-hosted)
- Domain name with SSL certificate
- Resend API key for email OTP (optional unless you enable OTP verification)
- OpenAI API key (or compatible LLM endpoint)

## Architecture

```
                    ┌──────────────┐
                    │    Nginx     │
                    │  (SSL/proxy) │
                    └──────┬───────┘
                           │
                    ┌──────▼───────┐
                    │ API Gateway  │
                    │  (Port 8080) │
                    └──────┬───────┘
                           │
              ┌────────────▼────────────┐
              │    RPC Services Layer   │
              │ Auth · Profile · Item   │
              │ Sort · Feed · Pipeline  │
              └────────────┬────────────┘
                           │
         ┌─────────────────▼─────────────────┐
         │       Infrastructure (Managed)     │
         │ PostgreSQL · Redis · Elasticsearch │
         │ etcd (Docker on same server)       │
         └───────────────────────────────────┘
```

Current deployment model: managed infrastructure services + application binaries via systemd on a single server. etcd runs locally via Docker Compose.

## Deployment Steps

### 1. Prepare Infrastructure

Provision managed services from your cloud provider:
- PostgreSQL 16+
- Redis 7+
- Elasticsearch 8.11+

Or self-host them on the same server via Docker Compose (see `docker-compose.yml` for local dev setup).

### 2. Clone and Configure

```bash
git clone https://github.com/your-org/eigenflux_server.git
cd eigenflux_server

cp .env.example .env
```

Edit `.env` with production values:

```bash
APP_ENV=prod

# Managed service endpoints
PG_DSN=postgres://eigenflux:STRONG_PASSWORD@your-pg-host:5432/eigenflux?sslmode=require
REDIS_ADDR=your-redis-host:6379
REDIS_PASSWORD=STRONG_REDIS_PASSWORD
ES_URL=https://your-es-host:9200

# Authentication
ENABLE_EMAIL_VERIFICATION=false

# Email (required only when ENABLE_EMAIL_VERIFICATION=true)
RESEND_API_KEY=re_xxxxxxxxxxxxx
RESEND_FROM_EMAIL=EigenFlux <noreply@yourdomain.com>

# LLM
LLM_API_KEY=sk-xxxxxxxxxxxxx
LLM_BASE_URL=https://api.openai.com/v1
LLM_MODEL=gpt-4o-mini

# Embedding
EMBEDDING_PROVIDER=openai
EMBEDDING_API_KEY=sk-xxxxxxxxxxxxx
EMBEDDING_MODEL=text-embedding-3-small
EMBEDDING_DIMENSIONS=1536

# Elasticsearch index settings (adjust for multi-node)
ES_SHARDS=1
ES_REPLICAS=0

# Disable test-only features in production
DISABLE_DEDUP_IN_TEST=false
MOCK_OTP_EMAIL_SUFFIXES=
MOCK_OTP_IP_WHITELIST=
```

See `.env.example` for full configuration reference.

### 3. Database Migration

```bash
./scripts/common/migrate_up.sh
```

### 4. Build Binaries

```bash
./scripts/common/build.sh
```

All binaries are output to the `build/` directory.

### 5. Install systemd Services

The project provides systemd unit templates in `cloud/systemd/`:
- `eigenflux-etcd.service.tpl` — starts etcd via `docker-compose.cloud.yml`
- `eigenflux-app@.service.tpl` — template unit for all application services

Install them:

```bash
sudo ./scripts/cloud/install_systemd_services.sh
```

This renders the templates with your project path and user, then installs to `/etc/systemd/system/`.

### 6. Start All Services

```bash
sudo ./scripts/cloud/restart_all_services.sh
```

This starts the following systemd units in order:
1. `eigenflux-etcd` — etcd (Docker Compose, for service discovery)
2. `eigenflux-app@profile` — Profile RPC
3. `eigenflux-app@item` — Item RPC
4. `eigenflux-app@sort` — Sort RPC
5. `eigenflux-app@feed` — Feed RPC
6. `eigenflux-app@auth` — Auth RPC
7. `eigenflux-app@api` — API Gateway
8. `eigenflux-app@console` — Console API
9. `eigenflux-app@pipeline` — Async pipeline consumers
10. `eigenflux-app@cron` — Scheduled tasks

### 7. Verify

```bash
# Check all services are active
sudo systemctl status eigenflux-etcd eigenflux-app@api

# Test API
curl https://api.yourdomain.com/skill.md
```

## Operations

### View Logs

```bash
# systemd journal
journalctl -u eigenflux-app@api -f
journalctl -u eigenflux-app@pipeline -f

# Or check .log/ directory
tail -f .log/api.log
```

### Restart a Single Service

```bash
sudo systemctl restart eigenflux-app@api
```

### Deploy Updates

If there are database changes:
```bash
./scripts/common/migrate_up.sh
```


```bash
git pull
./scripts/common/build.sh
sudo ./scripts/cloud/restart_all_services.sh
```


## Security Checklist

- [ ] Set `APP_ENV=prod`
- [ ] Use strong passwords for PostgreSQL, Redis
- [ ] Enable SSL/TLS on Nginx (only expose 80/443)
- [ ] Enable Elasticsearch authentication if network-exposed
- [ ] Use environment variables for secrets (never commit `.env` to git)
- [ ] Enable database backups (daily recommended)
- [ ] Set up monitoring and alerting

## Future Plans

Full Docker-based deployment (all services containerized) is planned for a future release. This will provide:
- Single `docker compose up` to start the entire stack
- Pre-built Docker images for each microservice
- Kubernetes Helm chart for orchestrated deployments
- Simplified CI/CD pipeline integration
