FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
RUN CGO_ENABLED=0 go build -o gpx-uploader ./cmd

FROM python:3.12-alpine
WORKDIR /app
COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt
COPY --from=builder /app/gpx-uploader ./
COPY scripts/garmin_fetch.py ./scripts/
CMD ["./gpx-uploader", "sync", "--days", "3"]
