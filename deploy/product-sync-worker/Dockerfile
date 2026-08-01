FROM node:22-bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
      ca-certificates \
      fonts-liberation \
      fonts-noto-cjk \
      wget \
      xauth \
      xvfb \
      xdotool \
    && wget -q -O /tmp/google-chrome.deb \
      https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb \
    && apt-get install -y --no-install-recommends /tmp/google-chrome.deb \
    && rm -f /tmp/google-chrome.deb \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --no-audit --no-fund
COPY index.js challenge-manager.js catalog-sync.js worker-utils.js healthcheck.js ./

RUN mkdir -p /data/challenge-sessions /data/browser-profiles \
    && chown -R node:node /app /data \
    && chmod 0700 /data/challenge-sessions /data/browser-profiles
USER node

ENV CHROME_PATH=/usr/bin/google-chrome-stable
CMD ["sh", "-c", "Xvfb :99 -screen 0 2560x1600x24 -ac +extension RANDR -nolisten tcp >/tmp/xvfb.log 2>&1 & export DISPLAY=:99; exec node index.js"]
