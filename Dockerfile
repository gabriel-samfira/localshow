FROM golang:latest as gobuilder
ADD . /localshow
WORKDIR /localshow
ENV CGO_ENABLED=0
RUN go build -ldflags="-s -w" -o ./localshowd ./cmd/localshowd

FROM scratch
COPY --from=gobuilder /localshow/localshowd /localshowd
CMD ["/localshowd", "--config", "/config/config.toml"]
