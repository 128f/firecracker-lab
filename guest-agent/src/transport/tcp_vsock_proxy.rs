use std::collections::HashMap;
use std::net::{TcpListener, TcpStream};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::thread::JoinHandle;
use std::time::Duration;
use vsock::{VsockAddr, VsockStream};

const ACCEPT_POLL_INTERVAL: Duration = Duration::from_millis(200);

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
struct TcpVsockProxyMapping {
    tcp_port: u16,
    cid: u32,
    vsock_port: u32,
}

struct ProxyHandle {
    stop: Arc<AtomicBool>,
    join: JoinHandle<()>,
}

#[derive(Default)]
pub struct ProxyRegistry {
    proxies: Mutex<HashMap<u16, ProxyHandle>>,
}

impl ProxyRegistry {
    pub fn new() -> Self {
        ProxyRegistry::default()
    }

    pub fn start(&self, tcp_port: u16, cid: u32, vsock_port: u32) -> anyhow::Result<()> {
        let mut proxies = self.proxies.lock().unwrap();
        if proxies.contains_key(&tcp_port) {
            anyhow::bail!("proxy already running on tcp port {tcp_port}");
        }

        let listener = TcpListener::bind(("0.0.0.0", tcp_port))
            .map_err(|e| anyhow::anyhow!("failed to bind tcp port {tcp_port}: {e}"))?;
        listener.set_nonblocking(true)?;

        let mapping = TcpVsockProxyMapping {
            tcp_port,
            cid,
            vsock_port,
        };
        let stop = Arc::new(AtomicBool::new(false));
        let join = {
            let stop = stop.clone();
            std::thread::spawn(move || run_listener(listener, mapping, stop))
        };

        proxies.insert(tcp_port, ProxyHandle { stop, join });
        Ok(())
    }

    pub fn stop(&self, tcp_port: u16) -> anyhow::Result<()> {
        let handle = self
            .proxies
            .lock()
            .unwrap()
            .remove(&tcp_port)
            .ok_or_else(|| anyhow::anyhow!("no proxy running on tcp port {tcp_port}"))?;

        handle.stop.store(true, Ordering::Relaxed);
        // join outside the lock (already dropped by this point) so the port
        // is guaranteed free before this returns
        let _ = handle.join.join();
        Ok(())
    }
}

fn run_listener(listener: TcpListener, mapping: TcpVsockProxyMapping, stop: Arc<AtomicBool>) {
    while !stop.load(Ordering::Relaxed) {
        match listener.accept() {
            Ok((tcp_stream, _addr)) => {
                std::thread::spawn(move || {
                    let _ = handle_connection(tcp_stream, mapping);
                });
            }
            Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => {
                std::thread::sleep(ACCEPT_POLL_INTERVAL);
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

#[cfg(test)]
mod tests {
    use super::*;

    // port 0 lets the OS pick a free ephemeral port for the real bind, while
    // the registry itself keys proxies by the *requested* port (0 here), so
    // these exercise the registry's own bookkeeping without racing other
    // tests over a fixed port number.
    #[test]
    fn start_twice_on_same_port_errors() {
        let registry = ProxyRegistry::new();
        registry.start(0, 2, 11434).unwrap();
        assert!(registry.start(0, 2, 11434).is_err());
        registry.stop(0).unwrap();
    }

    #[test]
    fn stop_then_start_again_succeeds() {
        let registry = ProxyRegistry::new();
        registry.start(0, 2, 11434).unwrap();
        registry.stop(0).unwrap();
        registry.start(0, 2, 11434).unwrap();
        registry.stop(0).unwrap();
    }

    #[test]
    fn stop_unknown_port_errors() {
        let registry = ProxyRegistry::new();
        assert!(registry.stop(0).is_err());
    }
}
