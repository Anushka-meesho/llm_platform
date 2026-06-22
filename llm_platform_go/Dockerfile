# syntax=docker/dockerfile:1

# ---- build ----
# Pure-Go (no cgo): modernc.org/sqlite + jackc/pgx, so we build a fully static
# binary and ship it on distroless/scratch. See docs/DEPLOY.md §5.
FROM golang:1.25 AS build
WORKDIR /src

# Cache modules first for faster rebuilds.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/llm-platform ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate     ./cmd/migrate \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bootstrap   ./cmd/bootstrap

# ---- runtime (distroless, static, non-root) ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/llm-platform /llm-platform
COPY --from=build /out/migrate      /migrate
COPY --from=build /out/bootstrap    /bootstrap
# pricing.json is loaded at boot; PRICING_PATH must be absolute in prod.
COPY pricing.json /etc/llm-platform/pricing.json
ENV PRICING_PATH=/etc/llm-platform/pricing.json

EXPOSE 8000
# API-only image. The Studio UI is a separate deployable. Run /migrate as a
# pre-deploy job (the prod server does NOT auto-migrate); /bootstrap is the
# once-per-environment first-run tool.
ENTRYPOINT ["/llm-platform"]
