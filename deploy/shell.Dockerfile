# AURA shell runtime (ADR-0065): a Linux sandbox where AURA runs server commands under the Core's watcher.
# No secrets, no AURA data, no Docker socket; internal network only (internet through the Core's egress proxy).
FROM python:3.12-slim-bookworm
ENV PYTHONDONTWRITEBYTECODE=1 PIP_DISABLE_PIP_VERSION_CHECK=1 DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      bash coreutils findutils grep sed gawk procps git curl wget jq unzip zip xz-utils file sqlite3 ripgrep \
      ca-certificates less tree bc \
 && rm -rf /var/lib/apt/lists/* \
 && pip install --no-cache-dir --root-user-action=ignore requests==2.32.3 pandas==2.2.3 openpyxl==3.1.5 \
      python-docx==1.1.2 pypdf==5.1.0 matplotlib==3.9.2 beautifulsoup4==4.12.3 pytest==8.3.4 \
 && useradd --system --uid 10004 --home-dir /work --shell /bin/bash runner \
 && mkdir -p /work /run/aura-shell && chown runner:runner /work /run/aura-shell && chmod 0755 /run/aura-shell
COPY shell/aura_shell /opt/aura-shell/aura_shell
COPY shell/tests /opt/aura-shell/tests
ENV PYTHONPATH=/opt/aura-shell
USER runner
WORKDIR /work
ENTRYPOINT ["python3", "-m", "aura_shell"]
