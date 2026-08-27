use std::net::{TcpListener, TcpStream};
use vsock::{VsockAddr, VsockStream};

use crate::boot::cmdline::TcpVsockProxyMapping;

pub fn spawn_proxies(mappings: &[TcpVsockProxyMapping]) {
    for mapping in mappings {
        let mapping = *mapping;
        std::thread::spawn(move || run_listener(mapping));
    }
}

fn run_listener(mapping: TcpVsockProxyMapping) {
    let listener = match TcpListener::bind(("0.0.0.0", mapping.tcp_port)) {
        Ok(l) => l,
        Err(e) => {
            eprintln!(
                "tcp_vsock_proxy: failed to bind TCP port {}: {e}",
                mapping.tcp_port
            );
            return;
        }
    };

    for stream in listener.incoming() {
        match stream {
            Ok(tcp_stream) => {
                std::thread::spawn(move || {
                    let _ = handle_connection(tcp_stream, mapping);
                });
            }
            Err(_e) => { /* TODO: do something */ }
        }
    }
}

fn handle_connection(tcp_stream: TcpStream, mapping: TcpVsockProxyMapping) -> anyhow::Result<()> {
    let vsock_stream = VsockStream::connect(&VsockAddr::new(mapping.cid, mapping.vsock_port))
        .inspect_err(|e| {
            eprintln!(
                "tcp_vsock_proxy: vsock connect to cid {} port {} failed: {e}",
                mapping.cid, mapping.vsock_port
            )
        })?;

    let mut vsock_reader = vsock_stream.try_clone()?;
    let mut tcp_writer = tcp_stream.try_clone()?;
    std::thread::spawn(move || {
        let _ = std::io::copy(&mut vsock_reader, &mut tcp_writer);
    });

    let mut tcp_reader = tcp_stream;
    let mut vsock_writer = vsock_stream;
    let _ = std::io::copy(&mut tcp_reader, &mut vsock_writer);

    Ok(())
}
