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
use stats::{Stats, Status};
use transport::pty_vsock::PtyVsockDispatcher;
use transport::vsock::VsockDispatcher;

fn main() {
    mount_system_paths(MountConfig::default()).unwrap();
    let sfd = connect_signalfd().unwrap();

    let mut vsock_dispatcher = VsockDispatcher::new().unwrap();
    let mut pty_vsock_dispatcher = PtyVsockDispatcher::new().unwrap();
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
                .with(
                    SignalSource::VsockListen,
                    vsock_dispatcher.vsock_listener.as_fd(),
                )
                .with(
                    SignalSource::PtyVsockListen,
                    pty_vsock_dispatcher.vsock_listener.as_fd(),
                )
                .with(SignalSource::Timer, timer_dispatcher.fd()),
        )
        .unwrap();

    stats.status.store(Status::Running as u8, Ordering::Relaxed);
    loop {
        // consume
        for source in epoller.get_events() {
            match source {
                Some(SignalSource::Signal) => drain_signals(sfd.as_fd()),
                Some(SignalSource::Timer) => timer_dispatcher.tick(),
                Some(SignalSource::VsockListen) => vsock_dispatcher.handle_events(&stats),
                Some(SignalSource::PtyVsockListen) => pty_vsock_dispatcher.handle_events(),
                None => {} // unknown tag; ignore
            }
        }
    }
}
