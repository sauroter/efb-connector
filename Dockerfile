FROM golang:1.25-alpine AS builder
ARG VERSION=dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-X main.version=${VERSION}" -o efb-connector ./cmd/server

FROM python:3.12-alpine
RUN apk add --no-cache su-exec && \
    addgroup -g 1001 -S app && adduser -u 1001 -S app -G app
WORKDIR /app
RUN mkdir -p /data
COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt
COPY --from=builder /app/efb-connector ./
COPY scripts/ ./scripts/
COPY templates/ ./templates/
COPY static/ ./static/
COPY entrypoint.sh ./
RUN chmod +x entrypoint.sh
EXPOSE 8080
ENTRYPOINT ["./entrypoint.sh"]
