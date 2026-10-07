FROM golang:1.23-alpine AS builder
WORKDIR /app
# Copy all files first so go mod tidy can find the dependencies in the code
COPY . .
RUN go mod tidy
RUN go build -o main .

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/main .
EXPOSE 8080 8081
CMD ["./main"]
