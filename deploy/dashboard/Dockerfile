# the dashboard's ssr server (M7b-D7), built from the repo root (a git archive on a server, the worktree in the dev stack;
# Dockerfile.dockerignore sends dashboard/ alone) on the pinned node image the Makefile names as NODE_IMAGE
# (dashboard-deploy.test.sh holds them equal). the runtime has the build, the production dependencies and serve/
# only, runs as node (uid 1000) and writes nothing, so it runs on a read-only root
FROM node:24.21.0-trixie-slim@sha256:173f125896c3b47ddf056734c7ea789d04595a6a08769a8f78e0df642781fb66 AS base
# corepack runs the pnpm package.json's packageManager names, checked against its sha512
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0 COREPACK_HOME=/opt/corepack PNPM_CONFIG_STORE_DIR=/var/cache/pnpm-store
WORKDIR /app
COPY dashboard/package.json dashboard/pnpm-lock.yaml dashboard/pnpm-workspace.yaml ./
RUN corepack pnpm --version

FROM base AS build
RUN --mount=type=cache,id=cadence-dashboard-pnpm,target=/var/cache/pnpm-store corepack pnpm install --frozen-lockfile
COPY dashboard/ ./
RUN corepack pnpm build

FROM base AS prod-deps
RUN --mount=type=cache,id=cadence-dashboard-pnpm,target=/var/cache/pnpm-store \
  NODE_ENV=production corepack pnpm install --frozen-lockfile --prod

FROM node:24.21.0-trixie-slim@sha256:173f125896c3b47ddf056734c7ea789d04595a6a08769a8f78e0df642781fb66
# react and its kin pick their production builds by it
ENV NODE_ENV=production
WORKDIR /app
COPY dashboard/package.json ./
COPY --from=prod-deps /app/node_modules ./node_modules
COPY --from=build /app/dist ./dist
COPY dashboard/serve ./serve
USER node
EXPOSE 3000
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=3 CMD ["node", "serve/healthcheck.ts"]
CMD ["node", "serve/main.ts"]
