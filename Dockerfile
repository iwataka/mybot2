FROM golang:1.17-alpine AS golang

WORKDIR /mybot2
COPY . .
RUN go build

CMD ./mybot2
