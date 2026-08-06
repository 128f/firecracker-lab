use rustix::termios::{OptionalActions, tcgetattr, tcsetattr};
use std::os::fd::BorrowedFd;

pub struct RawTerminal {
    fd: BorrowedFd<'static>,
    orig: rustix::termios::Termios,
}
impl RawTerminal {
    pub fn make_raw(fd: BorrowedFd<'static>) -> anyhow::Result<Self> {
        let orig = tcgetattr(fd)?;
        let mut raw = orig.clone();
        raw.make_raw();
        tcsetattr(fd, OptionalActions::Flush, &raw)?;
        Ok(RawTerminal { fd, orig })
    }

    pub fn make_raw_stdin() -> anyhow::Result<Self> {
        let stdin = unsafe { BorrowedFd::borrow_raw(0) };
        RawTerminal::make_raw(stdin)
    }

    pub fn restore(&mut self) {
        // TODO: how about making this stick and then non-op?
        let _ = tcsetattr(self.fd, OptionalActions::Now, &self.orig);
    }
}
impl Drop for RawTerminal {
    fn drop(&mut self) {
        self.restore();
    }
}
