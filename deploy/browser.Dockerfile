# AURA browser runtime (Blueprint 8.8.1, P4, ADR-0039): Playwright + Chromium headless behind the Core's egress proxy.
# Built only from this repository plus pinned upstream packages (Playwright: Apache-2.0; Chromium: BSD).
FROM python:3.12-slim-bookworm
ENV PYTHONDONTWRITEBYTECODE=1 PIP_DISABLE_PIP_VERSION_CHECK=1 PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright
RUN pip install --no-cache-dir --root-user-action=ignore playwright==1.49.1 \
 && python -m playwright install --with-deps chromium \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10003 --home-dir /tmp --shell /usr/sbin/nologin aura-browser \
 && mkdir -p /run/aura-browser && chown aura-browser:aura-browser /run/aura-browser && chmod 0755 /run/aura-browser
COPY browser/aura_browser /opt/aura-browser/aura_browser
ENV PYTHONPATH=/opt/aura-browser
USER aura-browser
ENTRYPOINT ["python3", "-m", "aura_browser"]
