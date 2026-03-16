FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o efb-connector ./cmd/server

FROM python:3.12-alpine
WORKDIR /app
RUN mkdir -p /data
COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt
COPY --from=builder /app/efb-connector ./
COPY scripts/ ./scripts/
COPY templates/ ./templates/
COPY static/ ./static/
EXPOSE 8080
CMD ["./efb-connector"]
