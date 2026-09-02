#![forbid(unsafe_code)]

/// Platform-neutral identity of a broker-issued desktop session.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SessionTicket {
    pub session_id: String,
    pub expires_at_unix: u64,
}

impl SessionTicket {
    pub fn is_expired_at(&self, now_unix: u64) -> bool {
        now_unix >= self.expires_at_unix
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ticket_expires_at_boundary() {
        let ticket = SessionTicket {
            session_id: "session-1".into(),
            expires_at_unix: 10,
        };
        assert!(!ticket.is_expired_at(9));
        assert!(ticket.is_expired_at(10));
    }
}
