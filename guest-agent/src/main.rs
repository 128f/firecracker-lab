use rustix::fs::{OFlags, fcntl_getfl, fcntl_setfl};
use std::io::stdin;
use std::os::fd::AsFd;
use std::sync::Arc;
use std::sync::atomic::Ordering;

mod boot;
mod proc_stats;
mod scheduler;
mod session;
mod stats;
mod transport;

const SAMPLE_INTERVAL: std::time::Duration = std::time::Duration::from_secs(1);

use boot::mount::{MountConfig, mount_system_paths};
use boot::signals::{SignalSource, connect_signalfd, drain_signals};
use scheduler::epoll::{PollConfig, Poller};
use scheduler::timerfd::TimerDispatcher;
use session::pty::{PtySession, setup_pty};
use session::terminal::RawTerminal;
use stats::{Stats, Status};
use transport::vsock::VsockDispatcher;

fn main() {
    let raw_terminal = RawTerminal::make_raw_stdin().unwrap();
    mount_system_paths(MountConfig::default()).unwrap();
    let sfd = connect_signalfd().unwrap();
    let (pty_fd, _) = setup_pty().unwrap();

    let mut vsock_dispatcher = VsockDispatcher::new().unwrap();
    let stats = Arc::new(Stats::new());
    let mut timer_dispatcher = TimerDispatcher::new(SAMPLE_INTERVAL).unwrap();
    timer_dispatcher.register({
        let stats = stats.clone();
        move || stats.sample()
    });

    let mut epoller = Poller::new();
    epoller
        .add_all(
            PollConfig::default()
                .with(SignalSource::Signal, sfd.as_fd())
                .with(SignalSource::Stdin, stdin().as_fd())
                .with(SignalSource::Pty, pty_fd.as_fd())
                .with(
                    SignalSource::VsockListen,
                    vsock_dispatcher.vsock_listener.as_fd(),
                )
                .with(SignalSource::Timer, timer_dispatcher.fd()),
        )
        .unwrap();

    // set non-blocking for the pty connections
    fcntl_setfl(&pty_fd, fcntl_getfl(&pty_fd).unwrap() | OFlags::NONBLOCK).unwrap();
    fcntl_setfl(stdin(), fcntl_getfl(stdin()).unwrap() | OFlags::NONBLOCK).unwrap();

    let mut pty_session = PtySession::new(pty_fd.try_clone().unwrap(), raw_terminal);
    stats.status.store(Status::Running as u8, Ordering::Relaxed);
    loop {
        // consume
        for source in epoller.get_events() {
            match source {
                Some(SignalSource::Signal) => drain_signals(sfd.as_fd()),
                Some(SignalSource::Stdin) => pty_session.handle_stdin(),
                Some(SignalSource::Pty) => pty_session.handle_pty(),
                Some(SignalSource::Timer) => timer_dispatcher.tick(),
                Some(SignalSource::VsockListen) => vsock_dispatcher.handle_events(&stats),
                None => {} // unknown tag; ignore
            }
        }
    }
}
