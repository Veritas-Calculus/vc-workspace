# syntax=docker/dockerfile:1.7
FROM node:22-alpine AS build

WORKDIR /src
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY apps/web/package.json ./apps/web/package.json
RUN pnpm install --frozen-lockfile --filter @vc-vdi/web...
COPY apps/web ./apps/web
COPY assets ./assets
RUN pnpm --filter @vc-vdi/web build

FROM nginxinc/nginx-unprivileged:1.29-alpine
COPY deploy/container/web.conf /etc/nginx/conf.d/default.conf
COPY --from=build /src/apps/web/dist /usr/share/nginx/html
EXPOSE 8080
