FROM node:20-alpine AS frontend-build
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.24-alpine AS backend-build
WORKDIR /app
COPY go.mod ./
COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /tb-market-parser .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates \
    && addgroup -S app \
    && adduser -S -G app app \
    && mkdir -p /data \
    && chown app:app /data
WORKDIR /app
COPY --from=backend-build /tb-market-parser ./tb-market-parser
COPY --from=frontend-build /app/frontend/dist ./frontend/dist
USER app
ENV HOST=0.0.0.0
ENV PORT=8080
ENV STATE_FILE=/data/market-state.json
EXPOSE 8080
ENTRYPOINT ["./tb-market-parser"]
