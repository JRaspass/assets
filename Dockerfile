FROM golang:1.12.6-alpine

RUN apk update \
 && apk upgrade \
 && apk add g++ git musl-dev

COPY go.mod go.sum main.go /assets/

RUN cd /assets \
 && go build -ldflags '-extldflags -static -linkmode external -s'

FROM scratch

COPY --from=0 /assets/assets /cmd

CMD ["/cmd"]
