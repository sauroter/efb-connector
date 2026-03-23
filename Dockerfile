FROM golang:1.25-alpine AS builder
ARG VERSION=dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-X main.version=${VERSION}" -o efb-connector ./cmd/server

FROM python:3.12-alpine
RUN addgroup -g 1001 -S app && adduser -u 1001 -S app -G app
WORKDIR /app
RUN mkdir -p /data && chown app:app /data
COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt
COPY --from=builder /app/efb-connector ./
COPY scripts/ ./scripts/
COPY templates/ ./templates/
COPY static/ ./static/
USER app
EXPOSE 8080
CMD ["./efb-connector"]
