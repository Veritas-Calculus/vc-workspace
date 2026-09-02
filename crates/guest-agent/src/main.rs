#![forbid(unsafe_code)]

use serde::Serialize;
use std::{
    env, fs,
    io::{self, Write},
    net::{SocketAddr, TcpStream},
    path::{Path, PathBuf},
    process::ExitCode,
    thread,
    time::{Duration, SystemTime, UNIX_EPOCH},
};

const VERSION: &str = env!("CARGO_PKG_VERSION");

#[derive(Clone, Debug, Eq, PartialEq)]
struct Config {
    state_dir: PathBuf,
    address: SocketAddr,
    interval: Duration,
    once: bool,
}

#[derive(Debug, Serialize)]
struct AgentState {
    schema_version: u8,
    agent_version: &'static str,
    hostname: String,
    os_family: &'static str,
    os_name: String,
    architecture: &'static str,
    heartbeat_unix: u64,
    desktop: DesktopState,
    capabilities: [&'static str; 3],
}

#[derive(Debug, Serialize)]
struct DesktopState {
    protocol: &'static str,
    address: String,
    ready: bool,
}

fn main() -> ExitCode {
    let config = match parse_args(env::args().skip(1)) {
        Ok(ParseResult::Run(config)) => config,
        Ok(ParseResult::Help) => {
            print_help();
            return ExitCode::SUCCESS;
        }
        Ok(ParseResult::Version) => {
            println!("vc-vdi-guest-agent {VERSION}");
            return ExitCode::SUCCESS;
        }
        Err(message) => {
            eprintln!("vc-vdi-guest-agent: {message}");
            return ExitCode::from(2);
        }
    };

    loop {
        if let Err(error) = update_state(&config, unix_time()) {
            eprintln!("vc-vdi-guest-agent: {error}");
            if config.once {
                return ExitCode::FAILURE;
            }
        }
        if config.once {
            return ExitCode::SUCCESS;
        }
        thread::sleep(config.interval);
    }
}

enum ParseResult {
    Run(Config),
    Help,
    Version,
}

fn parse_args(args: impl IntoIterator<Item = String>) -> Result<ParseResult, String> {
    let mut config = Config {
        state_dir: default_state_dir(),
        address: "127.0.0.1:3389"
            .parse()
            .expect("constant socket address is valid"),
        interval: Duration::from_secs(15),
        once: false,
    };
    let mut args = args.into_iter();
    while let Some(arg) = args.next() {
        match arg.as_str() {
            "--state-dir" => {
                config.state_dir = PathBuf::from(args.next().ok_or("--state-dir requires a path")?);
            }
            "--rdp-address" => {
                config.address = args
                    .next()
                    .ok_or("--rdp-address requires host:port")?
                    .parse()
                    .map_err(|_| "--rdp-address must be a numeric IP and port")?;
            }
            "--interval-seconds" => {
                let seconds: u64 = args
                    .next()
                    .ok_or("--interval-seconds requires a number")?
                    .parse()
                    .map_err(|_| "--interval-seconds must be a number")?;
                if !(5..=300).contains(&seconds) {
                    return Err("--interval-seconds must be between 5 and 300".into());
                }
                config.interval = Duration::from_secs(seconds);
            }
            "--once" => config.once = true,
            "--help" | "-h" => return Ok(ParseResult::Help),
            "--version" | "-V" => return Ok(ParseResult::Version),
            _ => return Err(format!("unknown argument {arg}")),
        }
    }
    Ok(ParseResult::Run(config))
}

fn print_help() {
    println!(
        "VC Workspace guest readiness agent\n\n\
Usage: vc-vdi-guest-agent [OPTIONS]\n\n\
Options:\n  --state-dir PATH          State directory\n  \
--rdp-address IP:PORT      Local RDP endpoint [default: 127.0.0.1:3389]\n  \
--interval-seconds N       Heartbeat interval, 5-300 [default: 15]\n  \
--once                     Write state once and exit\n  \
-h, --help                 Print help\n  \
-V, --version              Print version"
    );
}

fn update_state(config: &Config, heartbeat_unix: u64) -> io::Result<()> {
    let ready = TcpStream::connect_timeout(&config.address, Duration::from_millis(750)).is_ok();
    write_state(config, heartbeat_unix, ready)
}

fn write_state(config: &Config, heartbeat_unix: u64, ready: bool) -> io::Result<()> {
    fs::create_dir_all(&config.state_dir)?;
    let state = AgentState {
        schema_version: 1,
        agent_version: VERSION,
        hostname: hostname(),
        os_family: env::consts::OS,
        os_name: os_name(),
        architecture: env::consts::ARCH,
        heartbeat_unix,
        desktop: DesktopState {
            protocol: "rdp",
            address: config.address.to_string(),
            ready,
        },
        capabilities: ["heartbeat", "desktop-readiness", "os-inventory"],
    };
    let payload = serde_json::to_vec_pretty(&state).map_err(io::Error::other)?;
    atomic_write(&config.state_dir.join("agent-state.json"), &payload)?;

    let ready_path = config.state_dir.join("desktop-ready");
    if ready {
        atomic_write(&ready_path, b"ready\n")?;
    } else if ready_path.exists() {
        fs::remove_file(ready_path)?;
    }
    Ok(())
}

fn atomic_write(path: &Path, payload: &[u8]) -> io::Result<()> {
    let temporary = path.with_extension("tmp");
    let mut file = fs::File::create(&temporary)?;
    file.write_all(payload)?;
    file.sync_all()?;
    if path.exists() {
        fs::remove_file(path)?;
    }
    fs::rename(temporary, path)
}

fn default_state_dir() -> PathBuf {
    if cfg!(windows) {
        env::var_os("PROGRAMDATA")
            .map(PathBuf::from)
            .unwrap_or_else(|| PathBuf::from(r"C:\ProgramData"))
            .join("VC Workspace")
            .join("Agent")
    } else {
        PathBuf::from("/var/lib/vc-vdi")
    }
}

fn hostname() -> String {
    env::var("COMPUTERNAME")
        .or_else(|_| env::var("HOSTNAME"))
        .ok()
        .filter(|value| !value.trim().is_empty())
        .or_else(|| fs::read_to_string("/etc/hostname").ok())
        .map(|value| value.trim().to_owned())
        .filter(|value| !value.is_empty())
        .unwrap_or_else(|| "unknown".into())
}

fn os_name() -> String {
    if cfg!(target_os = "linux") {
        if let Ok(contents) = fs::read_to_string("/etc/os-release") {
            for line in contents.lines() {
                if let Some(value) = line.strip_prefix("PRETTY_NAME=") {
                    return value.trim_matches('"').replace("\\\"", "\"");
                }
            }
        }
    }
    env::consts::OS.to_owned()
}

fn unix_time() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn arguments_are_validated() {
        let parsed = parse_args([
            "--state-dir".into(),
            "/tmp/vc-vdi-test".into(),
            "--interval-seconds".into(),
            "30".into(),
            "--once".into(),
        ])
        .expect("arguments parse");
        let ParseResult::Run(config) = parsed else {
            panic!("expected runnable config")
        };
        assert_eq!(config.state_dir, PathBuf::from("/tmp/vc-vdi-test"));
        assert_eq!(config.interval, Duration::from_secs(30));
        assert!(config.once);
        assert!(parse_args(["--interval-seconds".into(), "1".into()]).is_err());
    }

    #[test]
    fn readiness_state_creates_the_control_plane_marker() {
        let temp = env::temp_dir().join(format!("vc-vdi-agent-test-{}", unix_time()));
        let config = Config {
            state_dir: temp.clone(),
            address: "127.0.0.1:3389".parse().unwrap(),
            interval: Duration::from_secs(15),
            once: true,
        };
        write_state(&config, 42, true).expect("write state");
        let state = fs::read_to_string(temp.join("agent-state.json")).expect("read state");
        let state: serde_json::Value = serde_json::from_str(&state).expect("parse state");
        assert_eq!(state["heartbeat_unix"], 42);
        assert_eq!(state["desktop"]["ready"], true);
        assert_eq!(
            fs::read_to_string(temp.join("desktop-ready")).unwrap(),
            "ready\n"
        );
        fs::remove_dir_all(temp).expect("remove test directory");
    }
}
