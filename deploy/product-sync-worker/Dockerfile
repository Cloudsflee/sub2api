ARG CAMOUFOX_VERSION=152.0.4-beta.28
ARG CAMOUFOX_SHA256=924f3109ccd6d47cd6a0384d67a345fadf975d48b6319f8dbbd5954c588982bd

# Keep the large browser archive out of the final image layers.  The release
# is pinned because Camoufox fingerprints and the Playwright Firefox protocol
# must change together.
FROM debian:bookworm-slim AS camoufox
ARG CAMOUFOX_VERSION
ARG CAMOUFOX_SHA256
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates wget unzip \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /tmp/camoufox /opt/camoufox \
    && wget -q -O /tmp/camoufox/browser.zip \
      "https://github.com/daijro/camoufox/releases/download/v${CAMOUFOX_VERSION}/camoufox-${CAMOUFOX_VERSION}-lin.x86_64.zip" \
    && echo "${CAMOUFOX_SHA256}  /tmp/camoufox/browser.zip" | sha256sum -c - \
    && unzip -q /tmp/camoufox/browser.zip -d /tmp/camoufox/extracted \
    && browser_path="$(find /tmp/camoufox/extracted -type f -name camoufox -print -quit)" \
    && test -n "${browser_path}" \
    && cp -a "$(dirname "${browser_path}")"/. /opt/camoufox/ \
    && chmod -R a+rX /opt/camoufox \
    && chmod +x /opt/camoufox/camoufox \
    && test -x /opt/camoufox/camoufox \
    && rm -rf /tmp/camoufox

FROM node:22-bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
      ca-certificates \
      fonts-liberation \
      fonts-noto-cjk \
      libasound2 \
      libatk-bridge2.0-0 \
      libatk1.0-0 \
      libcairo2 \
      libcups2 \
      libdbus-1-3 \
      libdbus-glib-1-2 \
      libdrm2 \
      libfontconfig1 \
      libfreetype6 \
      libgbm1 \
      libglib2.0-0 \
      libgtk-3-0 \
      libnspr4 \
      libnss3 \
      libpango-1.0-0 \
      libpangocairo-1.0-0 \
      libpci3 \
      libx11-6 \
      libx11-xcb1 \
      libxcb1 \
      libxcomposite1 \
      libxdamage1 \
      libxext6 \
      libxfixes3 \
      libxi6 \
      libxkbcommon0 \
      libxrandr2 \
      libxrender1 \
      libxshmfence1 \
      libxss1 \
      libxt6 \
      wget \
      xauth \
      xvfb \
      xdotool \
    && wget -q -O /tmp/google-chrome.deb \
      https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb \
    && apt-get install -y --no-install-recommends /tmp/google-chrome.deb \
    && rm -f /tmp/google-chrome.deb \
    && rm -rf /var/lib/apt/lists/*

COPY --from=camoufox /opt/camoufox /opt/camoufox

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --no-audit --no-fund
COPY index.js adaptive-rate.js challenge-manager.js catalog-sync.js shop-origins.js worker-utils.js healthcheck.js start-worker.sh ./

RUN mkdir -p /data/challenge-sessions /data/browser-profiles \
    && chmod 0755 /app/start-worker.sh \
    && chown -R node:node /app /data \
    && chmod 0700 /data/challenge-sessions /data/browser-profiles
USER node

ENV CHROME_PATH=/usr/bin/google-chrome-stable \
    CAMOUFOX_PATH=/opt/camoufox/camoufox \
    CAMOUFOX_FIREFOX_VERSION=152.0 \
    PRODUCT_SYNC_BROWSER=camoufox
CMD ["/app/start-worker.sh"]
