use rustix::fs::{OFlags, fcntl_getfl, fcntl_setfl};
use std::io::stdin;
use std::os::fd::AsFd;
use std::sync::Arc;
use std::sync::atomic::Ordering;

mod epoll;
mod mount;
mod proc_stats;
mod protocol;
mod pty;
mod signals;
mod stats;
mod terminal;
mod timerfd;
mod vsock_server;

const SAMPLE_INTERVAL: std::time::Duration = std::time::Duration::from_secs(1);

use epoll::EpollGrid;
use mount::mount_system_paths;
use pty::{PtyStatus, forward_pty, forward_stdin, setup_pty};
use signals::{SignalSource, connect_signalfd, drain_signals};
use stats::{Stats, Status};
use terminal::RawTerminal;
use timerfd::{connect_timerfd, drain_timerfd};
use vsock_server::{VsockConnectionHandler, VsockDispatcher};

fn main() {
    let mut raw_terminal = RawTerminal::make_raw_stdin().unwrap();
    mount_system_paths();
    let sfd = connect_signalfd().unwrap();
    let (pty_fd, _) = setup_pty().unwrap();

    let mut vsock_dispatcher = VsockDispatcher::new().unwrap();
    let stats = Arc::new(Stats::new());
    let timer_fd = connect_timerfd(SAMPLE_INTERVAL).unwrap();

    let mut epoller = EpollGrid::new();
    epoller
        .add_epoll(SignalSource::Signal, sfd.as_fd())
        .unwrap();
    epoller
        .add_epoll(SignalSource::Stdin, stdin().as_fd())
        .unwrap();
    epoller
        .add_epoll(SignalSource::Pty, pty_fd.as_fd())
        .unwrap();
    epoller
        .add_epoll(
            SignalSource::VsockListen,
            vsock_dispatcher.vsock_listener.as_fd(),
        )
        .unwrap();
    epoller
        .add_epoll(SignalSource::Timer, timer_fd.as_fd())
        .unwrap();

    // set non-blocking for the pty connections
    fcntl_setfl(&pty_fd, fcntl_getfl(&pty_fd).unwrap() | OFlags::NONBLOCK).unwrap();
    fcntl_setfl(stdin(), fcntl_getfl(stdin()).unwrap() | OFlags::NONBLOCK).unwrap();

    let pty_fd = pty_fd.try_clone().unwrap();
    stats.status.store(Status::Running as u8, Ordering::Relaxed);
    loop {
        // consume
        for source in epoller.get_events() {
            match source {
                Some(SignalSource::Signal) => drain_signals(sfd.as_fd()),
                Some(SignalSource::Stdin) => forward_stdin(pty_fd.as_fd()),
                Some(SignalSource::Pty) => match forward_pty(pty_fd.as_fd()) {
                    PtyStatus::Ok => {}
                    PtyStatus::HungUp => raw_terminal.restore(),
                },
                Some(SignalSource::Timer) => {
                    drain_timerfd(timer_fd.as_fd());
                    stats.sample();
                }
                Some(SignalSource::VsockListen) => {
                    for (stream, addr) in vsock_dispatcher.accept_connections() {
                        let stats = stats.clone();
                        std::thread::spawn(move || {
                            let mut handler = VsockConnectionHandler::new(stream, addr);
                            let Ok(prefix) = handler.read_length_prefix() else {
                                return;
                            };
                            let Ok(payload) = handler.extract_payload(prefix) else {
                                return;
                            };
                            let Ok(response) = protocol::dispatch(payload, &stats) else {
                                return;
                            };
                            let _ = handler.send_response(response);
                        });
                    }
                }
                None => {} // unknown tag; ignore
            }
        }
    }
}
