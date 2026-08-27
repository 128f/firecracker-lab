use serde::Deserialize;
use std::fs;
use std::os::fd::{AsFd, OwnedFd};
use std::process::Child;

#[derive(Deserialize)]
struct ImageConfig {
    #[serde(default = "default_cmd")]
    cmd: Vec<String>,
    #[serde(default)]
    env: Vec<String>,
    #[serde(default)]
    working_dir: String,
}

fn default_cmd() -> Vec<String> {
    vec!["/bin/sh".to_owned()]
}

pub fn spawn_pty_process() -> anyhow::Result<(OwnedFd, Child)> {
    let config_file = fs::read_to_string("/etc/labctl/image-config.json")?;
    let config: ImageConfig = serde_json::from_str(&config_file)?;

    let (pty, pts) = pty_process::blocking::open()?;
    let pty_fd: OwnedFd = rustix::io::dup(pty.as_fd())?;

    let mut command = pty_process::blocking::Command::new(&config.cmd[0]);

    command = command.args(&config.cmd[1..]);

    let env_vars = config.env.iter().filter_map(|kv| kv.split_once("="));

    for (key, value) in env_vars {
        command = command.env(key, value);
    }

    let child = command.spawn(pts)?;

    Ok((pty_fd, child))
}
