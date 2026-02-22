FROM node:22-alpine AS build

WORKDIR /app

COPY web/package*.json ./
RUN npm ci

COPY web/ ./
RUN npm run build

FROM caddy:2.9-alpine

COPY deploy/Caddyfile /etc/caddy/Caddyfile
COPY --from=build /app/dist /srv

EXPOSE 80
