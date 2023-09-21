FROM golang:latest as builder
WORKDIR /app
COPY ["Makefile", "go.*", "*.go", "./"]
RUN make release build

FROM debian:latest
COPY --from=builder --chmod=777 /app/app /
ENTRYPOINT ["/app"]