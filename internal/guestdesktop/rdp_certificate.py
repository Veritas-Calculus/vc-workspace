"""Read the local RDP listener's leaf certificate over the trusted QGA channel.

No user credentials, filesystem certificates, redirects, or remote target input.
The control plane validates the returned DER and establishes the trust pin; this
local discovery handshake is not a user connection and never sends RDP login data.
"""

import base64
import signal
import socket
import ssl
import struct
import sys
import time


def discover(port=3389):
    deadline = time.monotonic() + 5

    def remaining():
        value = deadline - time.monotonic()
        if value <= 0:
            raise TimeoutError()
        return value

    with socket.create_connection(("127.0.0.1", port), timeout=remaining()) as raw:
        # MS-RDPBCGR 2.2.1.1.1: TLS or CredSSP. Never negotiate plain RDP.
        raw.sendall(bytes.fromhex("030000130ee000000000000100080003000000"))
        response = bytearray()
        while len(response) < 19:
            raw.settimeout(remaining())
            part = raw.recv(19 - len(response))
            if not part:
                raise ValueError()
            response.extend(part)
        if (response[:6] != bytes.fromhex("030000130ed0")
                or response[10] != 0 or response[11] != 2
                or response[13:15] != b"\x08\x00"
                or struct.unpack("<I", response[15:19])[0] not in (1, 2)):
            raise ValueError()
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
        context.minimum_version = ssl.TLSVersion.TLSv1_2
        # Trust is the privileged loopback observation transported by PVE/QGA,
        # not a self-signed Guest's chain or a localhost TOFU record.
        context.check_hostname = False
        context.verify_mode = ssl.CERT_NONE
        raw.settimeout(remaining())
        with context.wrap_socket(raw) as secure:
            certificate = secure.getpeercert(binary_form=True)
            if not certificate or len(certificate) > 12288:
                raise ValueError()
            return base64.b64encode(certificate).decode("ascii")


if __name__ == "__main__":
    signal.alarm(7)  # QGA request cancellation must not leave a stuck probe.
    try:
        print(discover())
    except Exception:
        sys.stderr.write("local RDP certificate unavailable\n")
        sys.exit(1)
