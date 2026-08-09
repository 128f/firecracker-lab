# --- build stage ---
FROM rust:1.95 AS build
WORKDIR /app
COPY . .
RUN apt-get update && apt-get install -y --no-install-recommends protobuf-compiler \
    && rm -rf /var/lib/apt/lists/*
RUN cargo build --release

# --- rootfs stage ---
FROM ubuntu:24.04
COPY --from=build /app/target/x86_64-unknown-linux-gnu/release/guest-agent /bin/guest-agent
