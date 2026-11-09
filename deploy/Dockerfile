# AURA runtime image. Separate from every Jarvis image; built only from this repo.
FROM golang:1.26-bookworm AS core
WORKDIR /src/core/go
COPY core/go/ ./
ARG VERSION=0.0.0-p0
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags "-s -w -X main.Version=${VERSION}" -o /out/aura ./cmd/aura

FROM python:3.12-slim AS runtime
RUN useradd --system --uid 10001 --home-dir /var/lib/aura aura \
 && mkdir -p /var/lib/aura /opt/aura/worker && chown aura:aura /var/lib/aura
COPY --from=core /out/aura /usr/local/bin/aura
COPY intelligence/python/aura_worker /opt/aura/worker/aura_worker
COPY contracts /opt/aura/contracts
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=/opt/aura/worker
USER aura
WORKDIR /var/lib/aura
ENTRYPOINT ["aura"]
CMD ["doctor"]
