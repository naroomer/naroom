// lib/crypto.js — NaCl keypair generation, encrypt, decrypt
import nacl from 'tweetnacl';

export function newKeypair() {
  const kp = nacl.box.keyPair();
  return {
    pub:  toHex(kp.publicKey),
    priv: toHex(kp.secretKey),
    _pub: kp.publicKey,
    _priv: kp.secretKey,
  };
}

export function sharedKey(myPrivHex, peerPubHex) {
  return nacl.box.before(fromHex(peerPubHex), fromHex(myPrivHex));
}

export function encrypt(text, myPrivHex, peerPubHex) {
  const key = sharedKey(myPrivHex, peerPubHex);
  const nonce = nacl.randomBytes(nacl.box.nonceLength);
  const box = nacl.box.after(new TextEncoder().encode(text), nonce, key);
  return { nonce: toHex(nonce), ciphertext: toHex(box) };
}

export function decrypt(nonceHex, ciphertextHex, myPrivHex, peerPubHex) {
  const key = sharedKey(myPrivHex, peerPubHex);
  const plain = nacl.box.open.after(fromHex(ciphertextHex), fromHex(nonceHex), key);
  if (!plain) return null;
  return new TextDecoder().decode(plain);
}

export function toHex(b) {
  return Array.from(b).map(x => x.toString(16).padStart(2, '0')).join('');
}
export function fromHex(h) {
  return new Uint8Array(h.match(/../g).map(x => parseInt(x, 16)));
}

// Generate a fake 300KB image payload (base64 jpeg-like data url)
export function fakeImageDataUrl(sizeBytes = 300_000) {
  const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';
  let s = '';
  while (s.length < sizeBytes) s += chars[(Math.random() * 64) | 0];
  return 'data:image/jpeg;base64,' + s.slice(0, sizeBytes);
}
