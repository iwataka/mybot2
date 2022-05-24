FROM golang:1.17-alpine AS golang

RUN apk add --no-cache git
WORKDIR /mybot2
COPY . .
RUN go build

CMD ./mybot2
