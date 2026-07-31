FROM node:22-alpine3.21

RUN apk add --no-cache chromium nss freetype harfbuzz ttf-freefont ca-certificates

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --no-audit --no-fund
COPY index.js challenge-manager.js catalog-sync.js worker-utils.js healthcheck.js ./

RUN mkdir -p /data/challenge-sessions \
    && chown -R node:node /app /data \
    && chmod 0700 /data/challenge-sessions
USER node

ENV CHROME_PATH=/usr/bin/chromium-browser
CMD ["node", "index.js"]
