use rustix::io::Errno;
use std::io::{Write, stdin};
use std::os::fd::{AsFd, BorrowedFd, OwnedFd};

pub fn setup_pty() -> pty_process::Result<(OwnedFd, OwnedFd)> {
    // TODO : s/unwrap/doinitrite/g
    let (pty, pts) = pty_process::blocking::open()?;
    let pty_fd: OwnedFd = rustix::io::dup(pty.as_fd())?;
    let pts_fd: OwnedFd = rustix::io::dup(pts.as_fd())?;
    pty_process::blocking::Command::new("/bin/sh")
        .spawn(pts)
        .unwrap();
    Ok((pty_fd, pts_fd))
}

pub fn forward_stdin(pty_fd: BorrowedFd) {
    let mut buf = [0u8; 4096];
    loop {
        match rustix::io::read(stdin(), &mut buf) {
            Ok(0) => break, // outside user EOF'd
            Ok(n) => {
                // n bytes to drain form stdin to master
                let mut off = 0;
                while off < n {
                    match rustix::io::write(pty_fd, &buf[off..n]) {
                        Ok(w) => off += w,
                        Err(Errno::INTR) => continue,
                        // TODO: can we lock up here?
                        Err(Errno::AGAIN) => continue,
                        Err(_e) => break, // master can't be written
                    }
                }
            }
            Err(Errno::AGAIN) => break, // stdin drained
            Err(Errno::INTR) => continue,
            Err(_e) => break,
        }
    }
}

pub enum PtyStatus {
    Ok,
    HungUp,
}

pub fn forward_pty(pty_fd: BorrowedFd) -> PtyStatus {
    let mut stdout = std::io::stdout().lock();
    let mut buf = [0u8; 4096];
    loop {
        match rustix::io::read(pty_fd, &mut buf) {
            Ok(0) => return PtyStatus::HungUp,
            Ok(n) => {
                if stdout.write_all(&buf[..n]).is_err() {
                    // yeah I know it's not ok
                    // but probably this just means a temp write failure
                    // like the writer refuses
                    return PtyStatus::Ok;
                };
                let _ = stdout.flush();
            }
            Err(Errno::IO) => return PtyStatus::HungUp,
            Err(Errno::AGAIN) => return PtyStatus::Ok, // spurious wakeup / drained; keep looping
            Err(Errno::INTR) => continue,              // interrupted; retry next loop
            Err(_e) => return PtyStatus::Ok,           // TODO: actually handle the error tho
        };
    }
}
