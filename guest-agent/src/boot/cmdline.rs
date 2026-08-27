const TCP_VSOCK_PROXY_PARAM: &str = "guest_agent.tcp_vsock_proxy=";

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct TcpVsockProxyMapping {
    pub tcp_port: u16,
    pub cid: u32,
    pub vsock_port: u32,
}

/// Reads /proc/cmdline and returns parsed guest_agent.tcp_vsock_proxy
/// mappings. Empty if the param is absent, empty, or nothing parses.
pub fn parse_tcp_vsock_proxy_mappings() -> Vec<TcpVsockProxyMapping> {
    let cmdline = std::fs::read_to_string("/proc/cmdline").unwrap_or_default();
    parse_mappings_from_str(&cmdline)
}

fn parse_mappings_from_str(cmdline: &str) -> Vec<TcpVsockProxyMapping> {
    let Some(value) = cmdline
        .split_ascii_whitespace()
        .find_map(|tok| tok.strip_prefix(TCP_VSOCK_PROXY_PARAM))
    else {
        return vec![];
    };

    value
        .split(',')
        .filter(|entry| !entry.is_empty())
        .filter_map(|entry| match parse_entry(entry) {
            Ok(mapping) => Some(mapping),
            Err(reason) => {
                eprintln!(
                    "guest_agent.tcp_vsock_proxy: skipping malformed entry '{entry}': {reason}"
                );
                None
            }
        })
        .collect()
}

fn parse_entry(entry: &str) -> Result<TcpVsockProxyMapping, &'static str> {
    let mut fields = entry.split(':');
    let (Some(tcp_port), Some(cid), Some(vsock_port), None) =
        (fields.next(), fields.next(), fields.next(), fields.next())
    else {
        return Err("expected <tcp_port>:<cid>:<vsock_port>");
    };

    Ok(TcpVsockProxyMapping {
        tcp_port: tcp_port.parse().map_err(|_| "invalid tcp_port")?,
        cid: cid.parse().map_err(|_| "invalid cid")?,
        vsock_port: vsock_port.parse().map_err(|_| "invalid vsock_port")?,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn absent_param_returns_empty() {
        assert_eq!(parse_mappings_from_str("console=ttyS0 quiet"), vec![]);
    }

    #[test]
    fn single_mapping() {
        assert_eq!(
            parse_mappings_from_str("guest_agent.tcp_vsock_proxy=11434:2:11434"),
            vec![TcpVsockProxyMapping {
                tcp_port: 11434,
                cid: 2,
                vsock_port: 11434,
            }]
        );
    }

    #[test]
    fn multiple_mappings() {
        assert_eq!(
            parse_mappings_from_str(
                "quiet guest_agent.tcp_vsock_proxy=11434:2:11434,8080:2:9090 console=ttyS0"
            ),
            vec![
                TcpVsockProxyMapping { tcp_port: 11434, cid: 2, vsock_port: 11434 },
                TcpVsockProxyMapping { tcp_port: 8080, cid: 2, vsock_port: 9090 },
            ]
        );
    }

    #[test]
    fn malformed_entry_skipped_alongside_valid() {
        assert_eq!(
            parse_mappings_from_str("guest_agent.tcp_vsock_proxy=bad,11434:2:11434,also:bad:x:y"),
            vec![TcpVsockProxyMapping { tcp_port: 11434, cid: 2, vsock_port: 11434 }]
        );
    }

    #[test]
    fn trailing_comma_ignored() {
        assert_eq!(
            parse_mappings_from_str("guest_agent.tcp_vsock_proxy=11434:2:11434,"),
            vec![TcpVsockProxyMapping { tcp_port: 11434, cid: 2, vsock_port: 11434 }]
        );
    }
}
