FROM node:22-alpine AS build

WORKDIR /app

COPY web/package*.json ./
RUN npm ci

COPY web/ ./
ARG PUBLIC_UMAMI_WEBSITE_ID=""
ARG PUBLIC_UMAMI_HOST_URL="https://kws.kivayan.com"
ENV PUBLIC_UMAMI_WEBSITE_ID=$PUBLIC_UMAMI_WEBSITE_ID
ENV PUBLIC_UMAMI_HOST_URL=$PUBLIC_UMAMI_HOST_URL
RUN npm run build

FROM caddy:2.9-alpine

COPY deploy/Caddyfile /etc/caddy/Caddyfile
COPY --from=build /app/dist /srv

EXPOSE 80
