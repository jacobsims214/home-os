/**
 * Zero-knowledge crypto utilities for the native secrets manager.
 *
 * All operations use the Web Crypto API (window.crypto.subtle) — no external
 * npm packages. The browser derives a key from the master password, encrypts
 * secrets before sending to the API, and decrypts them on display. The API
 * never sees plaintext.
 *
 * Algorithm choices (see architecture/secrets-manager-research.md):
 *   - Key derivation: PBKDF2, 100,000 iterations, SHA-256
 *   - Encryption:     AES-256-GCM (authenticated, 12-byte IV)
 *   - Key verification: base64(SHA-256(exported key)) sent as `verifier`;
 *     the API stores only argon2id(verifier). See computeVerifier.
 */

// --- base64 helpers (binary-safe) --------------------------------------------

/** Encode an ArrayBuffer to a base64 string. */
function bufferToBase64(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer);
  let binary = "";
  for (let i = 0; i < bytes.byteLength; i++) {
    binary += String.fromCharCode(bytes[i]);
  }
  return btoa(binary);
}

/** Decode a base64 string to an ArrayBuffer-backed Uint8Array. */
function base64ToBytes(b64: string): Uint8Array<ArrayBuffer> {
  const binary = atob(b64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

/** Encode a UTF-8 string to bytes (ArrayBuffer-backed for Web Crypto compat). */
function stringToBytes(s: string): Uint8Array<ArrayBuffer> {
  const encoded = new TextEncoder().encode(s);
  // Copy into a fresh ArrayBuffer-backed buffer so it satisfies BufferSource
  // under TS 5.7+ stricter Uint8Array<ArrayBufferLike> typing.
  const buf = new ArrayBuffer(encoded.byteLength);
  const view = new Uint8Array(buf);
  view.set(encoded);
  return view;
}

/** Decode bytes as a UTF-8 string. */
function bytesToString(bytes: Uint8Array): string {
  return new TextDecoder().decode(bytes);
}

// --- PBKDF2 parameters -------------------------------------------------------

const PBKDF2_ITERATIONS = 100_000;
const SALT_LENGTH = 16; // bytes
const IV_LENGTH = 12; // bytes (AES-GCM standard nonce)

// --- Public API --------------------------------------------------------------

/**
 * Derive the raw 32 bytes of the vault key from a master password + salt using
 * PBKDF2. The AES key and the verifier both come from this one output, which
 * is what lets the verifier exist without ever exporting a key object (#4575).
 */
async function deriveKeyBits(
  password: string,
  saltBase64: string,
): Promise<Uint8Array> {
  const salt = base64ToBytes(saltBase64);
  const keyMaterial = await crypto.subtle.importKey(
    "raw",
    stringToBytes(password),
    { name: "PBKDF2" },
    false,
    ["deriveBits"],
  );

  const bits = await crypto.subtle.deriveBits(
    {
      name: "PBKDF2",
      salt,
      iterations: PBKDF2_ITERATIONS,
      hash: "SHA-256",
    },
    keyMaterial,
    256,
  );
  return new Uint8Array(bits);
}

/**
 * Derive an AES-GCM 256-bit key from a master password + salt using PBKDF2.
 *
 * @returns An EXTRACTABLE CryptoKey usable for AES-GCM encrypt/decrypt.
 *          Extractable only so computeVerifier() (tests, tools) can export and
 *          hash it. The app uses deriveKeyAndVerifier(), whose key is
 *          non-extractable.
 */
export async function deriveKey(
  password: string,
  saltBase64: string,
): Promise<CryptoKey> {
  const raw = await deriveKeyBits(password, saltBase64);
  const key = await crypto.subtle.importKey(
    "raw",
    raw,
    { name: "AES-GCM", length: 256 },
    /* extractable */ true,
    ["encrypt", "decrypt"],
  );
  raw.fill(0);
  return key;
}

/**
 * Derive the vault key AND its verifier in a single KDF pass, with the key
 * created NON-EXTRACTABLE (#4575).
 *
 * The verifier is base64(SHA-256(the same 32 PBKDF2 bytes)) — byte-identical
 * to computeVerifier(), so every household's stored argon2id(verifier) still
 * matches and nobody is locked out. The difference is that the CryptoKey put
 * in the store can no longer be exported, so a script that reaches the store
 * cannot lift the raw key material.
 */
export async function deriveKeyAndVerifier(
  password: string,
  saltBase64: string,
): Promise<{ key: CryptoKey; verifier: string }> {
  const raw = await deriveKeyBits(password, saltBase64);
  try {
    const digest = await crypto.subtle.digest("SHA-256", raw);
    const verifier = bufferToBase64(digest);
    const key = await crypto.subtle.importKey(
      "raw",
      raw,
      { name: "AES-GCM", length: 256 },
      /* extractable */ false,
      ["encrypt", "decrypt"],
    );
    return { key, verifier };
  } finally {
    raw.fill(0);
  }
}

/**
 * Encrypt plaintext with AES-256-GCM.
 *
 * @returns base64-encoded ciphertext and the base64-encoded IV used.
 */
export async function encrypt(
  key: CryptoKey,
  plaintext: string,
): Promise<{ ciphertext: string; iv: string }> {
  const iv = crypto.getRandomValues(new Uint8Array(IV_LENGTH));
  const encoded = stringToBytes(plaintext);

  const cipherBuf = await crypto.subtle.encrypt(
    { name: "AES-GCM", iv },
    key,
    encoded,
  );

  return {
    ciphertext: bufferToBase64(cipherBuf),
    iv: bufferToBase64(iv.buffer),
  };
}

/**
 * Decrypt an AES-256-GCM ciphertext.
 *
 * @param ciphertextBase64 Base64-encoded ciphertext.
 * @param ivBase64         Base64-encoded 12-byte IV.
 * @returns The UTF-8 plaintext string.
 */
export async function decrypt(
  key: CryptoKey,
  ciphertextBase64: string,
  ivBase64: string,
): Promise<string> {
  const ciphertext = base64ToBytes(ciphertextBase64);
  const iv = base64ToBytes(ivBase64);

  const plainBuf = await crypto.subtle.decrypt(
    { name: "AES-GCM", iv },
    key,
    ciphertext,
  );

  return bytesToString(new Uint8Array(plainBuf));
}

/** Generate a random 16-byte salt, base64-encoded. */
export function generateSalt(): string {
  const salt = crypto.getRandomValues(new Uint8Array(SALT_LENGTH));
  return bufferToBase64(salt.buffer);
}

/** Generate a random 12-byte IV, base64-encoded. */
export function generateIV(): string {
  const iv = crypto.getRandomValues(new Uint8Array(IV_LENGTH));
  return bufferToBase64(iv.buffer);
}

/**
 * Compute the key verifier sent to POST /api/v1/secrets/setup and /verify as
 * `verifier`: base64(SHA-256(raw key bytes)). The API never stores it as is;
 * it keeps only argon2id(verifier) (migration 051, #4440) and compares in
 * constant time, so the server never receives the key or the password.
 *
 * DO NOT CHANGE THIS COMPUTATION. Every household that has already set up a
 * master password has argon2id(this exact value) (or, before its first unlock
 * after 051, this exact value) stored server-side. A different derivation
 * (HMAC, domain separation, another hash) would lock all of them out. If it
 * ever has to change, add a verifier version the API stores alongside it and
 * upgrade on unlock; see KB architecture/secrets-management.md.
 *
 * computeVerifier needs an EXPORTABLE key and is kept for tests and tools.
 * The app path is deriveKeyAndVerifier(), which hashes the same PBKDF2 bytes
 * before importing a non-extractable AES key (#4575). Either way the value is
 * base64(SHA-256(same 32 bytes)) — never change that.
 */
export async function computeVerifier(key: CryptoKey): Promise<string> {
  const raw = await crypto.subtle.exportKey("raw", key);
  const digest = await crypto.subtle.digest("SHA-256", raw);
  return bufferToBase64(digest);
}
