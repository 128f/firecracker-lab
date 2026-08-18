use std::fs::File;
use vsock::{VsockAddr, VsockListener, VsockStream};

use crate::session::pty::spawn_pty_process;

const VMADDR_CID_ANY: u32 = u32::MAX;
const PTY_VSOCK_PORT: u32 = 1235;

pub struct PtyVsockDispatcher {
    pub vsock_listener: VsockListener,
}

impl PtyVsockDispatcher {
    pub fn new() -> std::io::Result<PtyVsockDispatcher> {
        let listener = VsockListener::bind(&VsockAddr::new(VMADDR_CID_ANY, PTY_VSOCK_PORT))?;
        listener.set_nonblocking(true)?;
        Ok(PtyVsockDispatcher {
            vsock_listener: listener,
        })
    }

    pub fn accept_connections(&mut self) -> Vec<(VsockStream, VsockAddr)> {
        let mut connections = vec![];
        loop {
            match self.vsock_listener.accept() {
                Ok((stream, addr)) => connections.push((stream, addr)),
                Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => break,
                Err(_e) => { /* TODO: do something */ }
            }
        }
        connections
    }

    /// Accepts every pending connection and hands each its own process/pty,
    /// pumped on its own threads.
    pub fn handle_events(&mut self) {
        for (stream, _addr) in self.accept_connections() {
            std::thread::spawn(move || {
                let _ = handle_pty_connection(stream);
            });
        }
    }
}

fn handle_pty_connection(stream: VsockStream) -> anyhow::Result<()> {
    let (pty_fd, _child) = spawn_pty_process()?;
    let pty = File::from(pty_fd);

    let mut pty_reader = pty.try_clone()?;
    let mut vsock_writer = stream.try_clone()?;
    std::thread::spawn(move || {
        let _ = std::io::copy(&mut pty_reader, &mut vsock_writer);
    });

    let mut pty_writer = pty;
    let mut vsock_reader = stream;
    let _ = std::io::copy(&mut vsock_reader, &mut pty_writer);

    Ok(())
}
