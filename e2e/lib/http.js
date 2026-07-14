// lib/http.js — session-aware API client
// After verifyWallet(), the session token is stored and used automatically
// for all protected endpoints via Authorization: Bearer <token>.
export class ApiClient {
  constructor(base) {
    this.base = base;
    this.tokens = {}; // wallet_address → { token, role }
  }

  // Returns { Authorization: 'Bearer ...' } for a wallet that has verified
  auth(wallet) {
    const s = this.tokens[wallet];
    if (!s) return {};
    return { 'Authorization': `Bearer ${s.token}` };
  }

  // Raw token string for a wallet (for WS Sec-WebSocket-Protocol)
  getToken(wallet) {
    return this.tokens[wallet]?.token ?? '';
  }

  async _req(method, path, data, wallet) {
    const headers = { ...(wallet ? this.auth(wallet) : {}) };
    if (data !== undefined) headers['Content-Type'] = 'application/json';
    const opts = { method, headers };
    if (data !== undefined) opts.body = JSON.stringify(data);
    const r = await fetch(this.base + path, opts);
    const body = await r.json().catch(() => ({}));
    return { status: r.status, body };
  }

  async get(path, wallet = null) { return this._req('GET', path, undefined, wallet); }
  async post(path, data, wallet = null) { return this._req('POST', path, data, wallet); }

  // ── Wallet / session ──────────────────────────────────────────────────────

  // Register wallet and get session token.
  // New flow: POST /session/init → session_token, then POST /wallet/register with Bearer.
  // If the wallet already has a stored token, skip /session/init and only call /wallet/register.
  async verifyWallet(wallet, currency = 'BTC', role) {
    // Step 1: get or create a principal/session
    let token = this.tokens[wallet]?.token ?? '';
    if (!token) {
      const initR = await this.post('/session/init', { role });
      if (initR.status !== 201 || !initR.body.session_token) return initR;
      token = initR.body.session_token;
      this.tokens[wallet] = { token, role };
    }

    // Step 2: link wallet to principal
    const headers = { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` };
    const resp = await fetch(this.base + '/wallet/register', {
      method: 'POST', headers,
      body: JSON.stringify({ wallet_address: wallet, currency, role }),
    });
    const body = await resp.json().catch(() => ({}));
    const r = { status: resp.status, body };

    if (resp.status !== 200) {
      // On failure, clear the stored token so next call retries from scratch
      if (resp.status !== 402) delete this.tokens[wallet];
    }
    return r;
  }

  // POST /session/refresh with the current token for wallet.
  // On success, updates the stored token to the new one.
  async sessionRefresh(wallet) {
    const oldToken = this.getToken(wallet);
    const r = await this._reqWithToken('POST', '/session/refresh', {}, oldToken);
    if (r.status === 200 && r.body.token) {
      // Update stored token to the refreshed one (old is revoked server-side)
      const session = this.tokens[wallet];
      if (session) this.tokens[wallet] = { ...session, token: r.body.token };
    }
    return r;
  }

  async sessionRevoke(wallet) {
    return this.post('/session/revoke', {}, wallet);
  }

  // Post with an explicit raw token (not from stored sessions)
  async _reqWithToken(method, path, data, rawToken) {
    const headers = { 'Content-Type': 'application/json' };
    if (rawToken) headers['Authorization'] = `Bearer ${rawToken}`;
    const r = await fetch(this.base + path, {
      method, headers, body: JSON.stringify(data),
    });
    const body = await r.json().catch(() => ({}));
    return { status: r.status, body };
  }

  // ── Listings ──────────────────────────────────────────────────────────────

  async createListing(wallet, city = 'new_york') {
    return this.post('/listing/create', {
      city, dependency_type: 'alcohol', help_type: 'crisis', urgency: 'urgent',
      languages: ['en'], currency: 'BTC',
    }, wallet);
  }

  async getListing(id) { return this.get(`/listing/${id}`); }
  async getBoard(city = 'new_york') { return this.get(`/board/${city}`); }

  // wallet = the listing owner's wallet (for auth)
  async getResponses(listingId, wallet) {
    return this.get(`/listing/${listingId}/responses`, wallet);
  }

  async getListingChatRoom(listingId, wallet) {
    return this.get(`/listing/${listingId}/chatroom`, wallet);
  }

  // ── Responses ─────────────────────────────────────────────────────────────

  // peerWallet for auth, peerPubkey for E2E
  async respond(listingId, peerWallet, peerPubkey) {
    return this.post(`/listing/${listingId}/respond`, { peer_pubkey: peerPubkey }, peerWallet);
  }

  async cancelResponse(responseId, peerWallet) {
    return this.post(`/response/${responseId}/cancel`, {}, peerWallet);
  }

  // clientWallet for auth, clientPubkey for E2E registration
  async acceptResponse(responseId, clientWallet, clientPubkey) {
    return this.post(`/response/${responseId}/accept`, {
      client_pubkey: clientPubkey, currency: 'BTC',
    }, clientWallet);
  }

  // ── Chat ──────────────────────────────────────────────────────────────────

  async getPeerChatroom(peerWallet, listingId) {
    return this.get(`/peer/chatroom?listing_id=${encodeURIComponent(listingId)}`, peerWallet);
  }

  // Returns { room_id, status, role, my_pubkey, peer_pubkey, ... }
  async getChatRoom(roomId, wallet) {
    return this.get(`/chat/${roomId}`, wallet);
  }

  // pubkey still required in body — handler identifies sender by pubkey for E2E attribution
  async pollSend(roomId, wallet, pubkey, nonce, ciphertext, msgType = 'text') {
    return this.post('/chat/poll/send', { room_id: roomId, pubkey, nonce, ciphertext, msg_type: msgType }, wallet);
  }

  async pollReceive(roomId, wallet, pubkey, since = 0) {
    return this.get(`/chat/poll/receive?room_id=${roomId}&pubkey=${encodeURIComponent(pubkey)}&since=${since}`, wallet);
  }

  async closeChat(roomId, wallet) {
    return this.post(`/chat/${roomId}/close`, {}, wallet);
  }

  // ── Misc ──────────────────────────────────────────────────────────────────

  async submitReview(token, rating) {
    return this.post('/review', { token, rating });
  }

  async invoiceStatus(invoiceId, wallet) {
    return this.get(`/invoice/${invoiceId}/status`, wallet);
  }

}
