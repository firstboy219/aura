# AURA runtime image; built only from this repository.
FROM golang:1.26-bookworm AS core
WORKDIR /src/core/go
COPY core/go/go.mod core/go/go.sum ./
RUN go mod download
COPY core/go/ ./
ARG VERSION=0.1.0-p1
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags "-s -w -X main.Version=${VERSION}" -o /out/aura ./cmd/aura

FROM python:3.12-slim AS runtime
ENV PYTHONDONTWRITEBYTECODE=1 PIP_DISABLE_PIP_VERSION_CHECK=1 AURA_CONFIG=/etc/aura/config.yaml
COPY intelligence/python /tmp/worker
RUN pip install --no-cache-dir --root-user-action=ignore /tmp/worker && rm -rf /tmp/worker \
 && useradd --system --uid 10001 --home-dir /var/lib/aura --shell /usr/sbin/nologin aura \
 && useradd --system --uid 10002 --home-dir /nonexistent --shell /usr/sbin/nologin aura-worker \
 && mkdir -p /var/lib/aura /run/aura-worker && chown aura:aura /var/lib/aura \
 && chown aura-worker:aura-worker /run/aura-worker && chmod 0755 /run/aura-worker
COPY --from=core /out/aura /usr/local/bin/aura
USER aura
WORKDIR /var/lib/aura
EXPOSE 7400
ENTRYPOINT ["aura"]
CMD ["serve"]
