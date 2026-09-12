// Read-only smoke test of the explicitly deployed public edge. Never issues a
// desktop ticket, sends a password or relaxes the system certificate trust.
import assert from 'node:assert/strict';
import https from 'node:https';

const origin = 'https://ws.infra.plz.ac';
try {
  for (const [route, expected] of [['/', 200], ['/api/v1/ready', 200], ['/mcp', 401],
    ['/internal/gateway/v1/ready', 404], ['/gateway/v1/redeem', 404]]) {
    const status = await new Promise((resolve, reject) => {
      const request = https.get(origin + route, {minVersion: 'TLSv1.3', rejectUnauthorized: true, timeout: 8000}, response => {
        assert.equal(response.socket.authorized, true);
        assert.equal(response.socket.getProtocol(), 'TLSv1.3');
        response.resume(); response.on('end', () => resolve(response.statusCode));
      });
      request.on('error', reject);
      request.on('timeout', () => request.destroy(new Error('HTTPS deadline')));
    });
    assert.equal(status, expected, route);
    console.log(`${route}: ${status}, trusted TLS 1.3`);
  }
  const redirect = await fetch('http://ws.infra.plz.ac/', {redirect: 'manual', signal: AbortSignal.timeout(8000)});
  assert.equal(redirect.status, 308);
  assert.equal(new URL(redirect.headers.get('location')).href, origin + '/');
  await new Promise((resolve, reject) => {
    const ws = new WebSocket('wss://ws.infra.plz.ac/gateway/v1/rdp', 'vc-workspace-rdp.v1');
    let opened = false;
    const timer = setTimeout(() => { ws.close(); reject(new Error('WSS deadline')); }, 8000);
    ws.onopen = () => {
      opened = true;
      if (ws.protocol !== 'vc-workspace-rdp.v1') { clearTimeout(timer); ws.close(); reject(new Error('Incorrect subprotocol')); return; }
      ws.send('invalid-ticket');
    };
    ws.onmessage = () => { clearTimeout(timer); ws.close(); reject(new Error('Invalid ticket received data')); };
    ws.onerror = () => {
      // The production relay intentionally closes invalid tickets immediately,
      // without a close-handshake grace period. Node can report an error here.
      if (!opened) { clearTimeout(timer); reject(new Error('WSS upgrade failed')); }
    };
    ws.onclose = () => { clearTimeout(timer); opened ? resolve() : reject(new Error('WSS never upgraded')); };
  });
  console.log('WSS negotiated exact protocol and closed the invalid ticket without forwarding data');
} catch (error) { console.error(error.message); process.exitCode = 1; }
