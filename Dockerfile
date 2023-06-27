# Build stage for dqlite lib and go app
FROM ubuntu:latest AS build
ARG DEBIAN_FRONTEND="noninteractive"
ENV TZ=UTC
ENV LD_LIBRARY_PATH=/usr/local/lib
ENV GOROOT=/usr/local/go
ENV GOPATH=/go
ENV GOBIN=/usr/local/bin/
ENV PATH=$GOPATH/bin:$GOROOT/bin:$PATH
RUN apt-get update && apt-get install -y git build-essential dh-autoreconf pkg-config libuv1-dev libsqlite3-dev liblz4-dev tcl8.6 wget
WORKDIR /opt
RUN git clone https://github.com/canonical/raft.git && \
    git clone https://github.com/canonical/dqlite.git && \
    git clone https://github.com/canonical/go-dqlite.git && \
    wget -c https://golang.org/dl/go1.20.5.linux-amd64.tar.gz -O - | tar -xzf - -C /usr/local
WORKDIR /opt/raft
RUN autoreconf -i && ./configure && make && make install
WORKDIR /opt/dqlite
RUN autoreconf -i && ./configure && make && make install

WORKDIR /opt/go-dqlite
RUN go get -d -v ./... && \
    export CGO_ENABLED=1; export CC=gcc; go install -tags libsqlite3 ./cmd/dqlite

WORKDIR /app
COPY . .
RUN go mod download
# RUN export CGO_ENABLED=1; export CC=gcc; go build -ldflags="-extldflags=-static" -o test-app *.go
RUN export CGO_ENABLED=1; export CC=gcc; go build -ldflags="-linkmode=external -s -w" -o test-app *.go
RUN mkdir lib; ldd /app/test-app | awk '/=>/ {print $(NF-1)}' | xargs -I {} cp -v {} /app/lib/

# Final stage
FROM ubuntu:latest
ENV TZ=UTC
ENV LD_LIBRARY_PATH=/usr/local/lib
WORKDIR /app
RUN mkdir /app/db
VOLUME /app/db
COPY --from=build /app/lib/ /usr/local/lib/
COPY --from=build /usr/local/bin/dqlite /usr/local/bin/
COPY --from=build /app/test-app .
CMD ["./test-app"]
