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
 *   - Key verification: SHA-256 hash of the exported key material
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
 * Derive an AES-GCM 256-bit key from a master password + salt using PBKDF2.
 *
 * @param password   The user's master password (UTF-8).
 * @param saltBase64 Base64-encoded salt (from generateSalt or the API).
 * @returns An extractable CryptoKey usable for AES-GCM encrypt/decrypt.
 *          Extractable so hashKey() can export + hash it for verification.
 */
export async function deriveKey(
  password: string,
  saltBase64: string,
): Promise<CryptoKey> {
  const salt = base64ToBytes(saltBase64);
  const keyMaterial = await crypto.subtle.importKey(
    "raw",
    stringToBytes(password),
    { name: "PBKDF2" },
    false,
    ["deriveKey"],
  );

  return crypto.subtle.deriveKey(
    {
      name: "PBKDF2",
      salt,
      iterations: PBKDF2_ITERATIONS,
      hash: "SHA-256",
    },
    keyMaterial,
    { name: "AES-GCM", length: 256 },
    /* extractable */ true,
    ["encrypt", "decrypt"],
  );
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
 * Hash a CryptoKey for verification. Exports the raw key material, computes
 * SHA-256, and returns the base64-encoded digest. The API stores this hash
 * and compares it on unlock to verify the user entered the correct password
 * without ever receiving the key itself.
 *
 * The key MUST be created with extractable=true (see deriveKey).
 */
export async function hashKey(key: CryptoKey): Promise<string> {
  const raw = await crypto.subtle.exportKey("raw", key);
  const digest = await crypto.subtle.digest("SHA-256", raw);
  return bufferToBase64(digest);
}
