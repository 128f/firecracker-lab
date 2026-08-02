# --- build stage ---
FROM golang:1.22 AS build
WORKDIR /src
RUN cat > main.go <<'EOF'
package main

import (
	"net/http"
	"log"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello from firecracker\n"))
	})
	log.Fatal(http.ListenAndServe(":8080", nil))
}
EOF
RUN go mod init web && CGO_ENABLED=0 go build -o /web main.go

# --- rootfs stage ---
FROM ubuntu:24.04
COPY --from=build /web /usr/local/bin/web

# This just does systemd has the pid 1
#FROM ubuntu:24.04
#RUN apt-get update && apt-get install -y --no-install-recommends \
#    systemd systemd-sysv \
#    iproute2 \
#    && rm -rf /var/lib/apt/lists/*
#
#
