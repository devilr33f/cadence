# the e2e's browser (M7b-D8): the pinned official playwright image, whose browsers match the @playwright/test the
# dashboard's lockfile pins (dashboard-deploy.test.sh holds the versions equal), with the dashboard's frozen install.
# the playwright image has no corepack, so the install is made in the pinned node image the Makefile names (NODE_IMAGE)
# by the pnpm package.json names, checked against its sha512. the specs are mounted at run time (compose.yml); the
# command reads the owner's password from its stdin into the container's own /tmp, so it is never on a mount or argument
FROM node:24.21.0-trixie-slim@sha256:173f125896c3b47ddf056734c7ea789d04595a6a08769a8f78e0df642781fb66 AS deps
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0 COREPACK_HOME=/opt/corepack PNPM_CONFIG_STORE_DIR=/var/cache/pnpm-store
WORKDIR /e2e
COPY dashboard/package.json dashboard/pnpm-lock.yaml dashboard/pnpm-workspace.yaml ./
RUN --mount=type=cache,id=cadence-dashboard-pnpm,target=/var/cache/pnpm-store corepack pnpm install --frozen-lockfile

FROM mcr.microsoft.com/playwright:v1.65.0-noble@sha256:9beb546099e11f52d62e505537914870e01a67e0e6b00ae41da4775324e73dbe
WORKDIR /e2e
COPY --from=deps /e2e/node_modules ./node_modules
COPY dashboard/package.json ./
ENV CADENCE_E2E_PASSWORD_FILE=/tmp/cadence-e2e/password
ENTRYPOINT ["sh", "-c", "umask 077 && mkdir -p /tmp/cadence-e2e && head -n 1 >\"$CADENCE_E2E_PASSWORD_FILE\" && exec node node_modules/@playwright/test/cli.js test -c e2e/playwright.config.ts \"$@\"", "playwright"]
CMD []
