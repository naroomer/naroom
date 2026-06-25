// lib/ws.js — WebSocket actor with bounded reconnect and terminal state detection
// Authentication: session token passed as Sec-WebSocket-Protocol header (Step 5).
import WebSocket from 'ws';
import { sleep } from './server.js';
import { encrypt, decrypt } from './crypto.js';

export class ChatWS {
  // token: session token for WS auth (Sec-WebSocket-Protocol)
  // wallet: wallet address for API calls in reconnect logic
  // myPubkey: X25519 pubkey — identifies "my" messages in history
  // privkey, peerPubkey: keypair for E2E decryption
  constructor(wsBase, roomId, token, wallet, myPubkey, privkey, peerPubkey) {
    this.wsBase = wsBase;
    this.roomId = roomId;
    this.token = token;
    this.wallet = wallet;
    this.myPubkey = myPubkey;
    this.privkey = privkey;
    this.peerPubkey = peerPubkey;
    this.ws = null;
    this.messages = [];
    this.systemEvents = [];
    this.closed = false;
    this.reconnectCount = 0;
    this.MAX_RECONNECTS = 3;
  }

  connect() {
    return new Promise((resolve, reject) => {
      const url = `${this.wsBase}/chat/ws?room_id=${this.roomId}`;
      // Token sent as Sec-WebSocket-Protocol — only way browser WS API can send auth material.
      // ws npm package sends it in the Sec-WebSocket-Protocol header.
      this.ws = new WebSocket(url, this.token ? [this.token] : []);

      this.ws.on('open', () => resolve());
      this.ws.on('error', reject);

      this.ws.on('message', (raw) => {
        try {
          const data = JSON.parse(raw);
          if (data.type === 'system') {
            this.systemEvents.push(data);
            return;
          }
          const text = decrypt(data.nonce, data.ciphertext, this.privkey, this.peerPubkey);
          const from = data.sender_pubkey === this.myPubkey ? 'me' : 'them';
          this.messages.push({ ...data, decrypted: text, from });
        } catch {}
      });

      this.ws.on('close', () => {
        if (!this.closed) {
          this.closed = true;
        }
      });
    });
  }

  send(text, msgType = 'text') {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) throw new Error('WS not open');
    const enc = encrypt(text, this.privkey, this.peerPubkey);
    this.ws.send(JSON.stringify({ ...enc, msg_type: msgType }));
  }

  close() {
    this.closed = true;
    if (this.ws) { this.ws.terminate(); this.ws = null; }
  }

  // Returns: { terminal: true, status } or throws if max reconnects exceeded
  async reconnectUntilTerminal(api) {
    for (let attempt = 1; attempt <= this.MAX_RECONNECTS; attempt++) {
      const r = await api.getChatRoom(this.roomId, this.wallet);
      if (r.status === 200 && (r.body.status === 'closed' || r.body.status === 'expired')) {
        return { terminal: true, status: r.body.status };
      }
      if (r.status === 404 || r.status === 403) {
        return { terminal: true, status: 'not_found' };
      }
      this.closed = false;
      try {
        await this.connect();
        await sleep(500);
        return { terminal: false, reconnected: true };
      } catch {}
      await sleep(1000);
    }
    throw new Error(`Reconnect loop did not reach terminal state after ${this.MAX_RECONNECTS} attempts`);
  }

  waitForSystemEvent(eventName, timeout = 10000) {
    const existing = this.systemEvents.find(e => e.event === eventName);
    if (existing) return Promise.resolve(existing);

    return new Promise((resolve, reject) => {
      const deadline = Date.now() + timeout;
      const check = setInterval(() => {
        const found = this.systemEvents.find(e => e.event === eventName);
        if (found) { clearInterval(check); resolve(found); }
        else if (Date.now() >= deadline) {
          clearInterval(check);
          reject(new Error(`Timeout waiting for system event: ${eventName}`));
        }
      }, 100);
    });
  }

  waitForMessage(timeout = 10000) {
    return new Promise((resolve, reject) => {
      const before = this.messages.length;
      const deadline = Date.now() + timeout;
      const check = setInterval(() => {
        if (this.messages.length > before) {
          clearInterval(check);
          resolve(this.messages[this.messages.length - 1]);
        }
        if (Date.now() >= deadline) {
          clearInterval(check);
          reject(new Error('Timeout waiting for WS message'));
        }
      }, 100);
    });
  }
}
