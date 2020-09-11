FROM golang:1.14 AS builder
ENV CGO_ENABLED 0
WORKDIR /go/src/app
ADD . .
RUN go build -mod vendor -o /auto-logtube-mapping
RUN go build -mod vendor -o /migrate-logtube-mapping ./migrate-logtube-mapping

FROM alpine:3.12
COPY --from=builder /auto-logtube-mapping /auto-logtube-mapping
COPY --from=builder /migrate-logtube-mapping /migrate-logtube-mapping
CMD ["/auto-logtube-mapping"]