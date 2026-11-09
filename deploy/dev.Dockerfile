# Dev/test image: Python 3.12 + Go 1.26 + uv, so Go integration tests can run the real Python worker.
FROM golang:1.26-bookworm AS go
FROM python:3.12-slim
COPY --from=go /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:/root/go/bin:$PATH PIP_DISABLE_PIP_VERSION_CHECK=1 CGO_ENABLED=0 GOFLAGS=-buildvcs=false
RUN pip install -q --root-user-action=ignore uv
