use prost::Message;
use std::io::{Read, Write};
use std::sync::Arc;
use vsock::{VsockAddr, VsockListener, VsockStream};

use crate::stats::Stats;
use crate::transport::dispatch;
use crate::transport::wire;

// VMADDR_CID_ANY = 0xFFFFFFFF (u32::MAX) — accept from any CID
const VMADDR_CID_ANY: u32 = u32::MAX;
const VSOCK_LISTEN_PORT: u32 = 1234;

pub struct VsockDispatcher {
    pub vsock_listener: VsockListener,
}

impl VsockDispatcher {
    pub fn new() -> std::io::Result<VsockDispatcher> {
        let listener = VsockListener::bind(&VsockAddr::new(VMADDR_CID_ANY, VSOCK_LISTEN_PORT))?;
        listener.set_nonblocking(true)?;
        Ok(VsockDispatcher {
            vsock_listener: listener,
        })
    }

    pub fn accept_connections(&mut self) -> Vec<(VsockStream, VsockAddr)> {
        let mut connections = vec![];
        loop {
            match self.vsock_listener.accept() {
                Ok((stream, addr)) => connections.push((stream, addr)),
                Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => {
                    // we're done collecting connections
                    break;
                }
                Err(_e) => { /* TODO: do something */ }
            }
        }
        connections
    }

    /// Accepts every pending connection and handles each on its own thread.
    pub fn handle_events(&mut self, stats: &Arc<Stats>) {
        for (stream, addr) in self.accept_connections() {
            let stats = stats.clone();
            std::thread::spawn(move || {
                let _ = handle_connection(stream, addr, &stats);
            });
        }
    }
}

fn handle_connection(stream: VsockStream, addr: VsockAddr, stats: &Stats) -> anyhow::Result<()> {
    let mut handler = VsockConnectionHandler::new(stream, addr);
    let prefix = handler.read_length_prefix()?;
    let payload = handler.extract_payload(prefix)?;
    let response = dispatch::dispatch(payload, stats)?;
    handler.send_response(response)
}

pub struct VsockConnectionHandler {
    stream: VsockStream,
    addr: VsockAddr,
}

impl VsockConnectionHandler {
    pub fn new(stream: VsockStream, addr: VsockAddr) -> VsockConnectionHandler {
        VsockConnectionHandler { stream, addr }
    }

    pub fn read_length_prefix(&mut self) -> std::io::Result<usize> {
        let mut buf = [0u8; 4];
        self.stream.read_exact(&mut buf)?;
        Ok(u32::from_be_bytes(buf) as usize)
    }

    pub fn extract_payload(&mut self, size: usize) -> anyhow::Result<wire::Request> {
        let mut buf = vec![0u8; size];
        self.stream.read_exact(&mut buf)?;
        Ok(wire::Request::decode(buf.as_slice())?)
    }

    pub fn send_response(&mut self, response: wire::StatusResponse) -> anyhow::Result<()> {
        let buffer = response.encode_to_vec();
        let bytes = (buffer.len() as u32).to_be_bytes();
        self.stream.write_all(&bytes)?;
        self.stream.write_all(&buffer)?;
        Ok(())
    }
}
