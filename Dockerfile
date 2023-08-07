FROM docker.io/golang:alpine AS builder
ARG LOCALSHOW_REF

LABEL stage=builder

RUN apk add musl-dev gcc libtool m4 autoconf g++ make libblkid util-linux-dev git linux-headers mingw-w64-gcc
RUN git config --global --add safe.directory /build

ADD . /build/localshow
RUN cd /build/localshow && git checkout ${LOCALSHOW_REF}

RUN cd /build/localshow && go build -o /bin/localshowd \
    -tags osusergo,netgo \
    -ldflags "-linkmode external -extldflags '-static' -s -w -X github.com/gabriel-samfira/localshow/cmd/localshowd/cmd.Version=$(git describe --tags --match='v[0-9]*' --dirty --always)" \
    /build/localshow/cmd/localshowd

FROM scratch

COPY --from=builder /bin/localshowd /bin/localshowd
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

ENTRYPOINT ["/bin/localshowd", "--config", "/etc/localshow/config.toml"]
