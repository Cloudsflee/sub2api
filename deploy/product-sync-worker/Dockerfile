FROM node:22-alpine3.21

RUN apk add --no-cache chromium nss freetype harfbuzz ttf-freefont ca-certificates

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --no-audit --no-fund
COPY index.js worker-utils.js ./

RUN mkdir -p /data && chown -R node:node /app /data
USER node

ENV CHROME_PATH=/usr/bin/chromium-browser
CMD ["node", "index.js"]
