FROM golang:1.14 AS builder
ENV CGO_ENABLED 0
WORKDIR /go/src/app
ADD . .
RUN go build -mod vendor -o /auto-logtube-mapping

FROM alpine:3.12
COPY --from=builder /auto-logtube-mapping /auto-logtube-mapping
CMD ["/autodown"]