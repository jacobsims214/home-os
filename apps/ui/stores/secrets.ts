/**
 * Secrets Zustand store — manages the in-memory encryption key lifecycle.
 *
 * The derived CryptoKey is held ONLY in memory. It is never persisted to
 * localStorage, sessionStorage, or cookies (this store deliberately does not
 * use zustand's `persist`). The master password and the verifier are local
 * variables of setup()/unlock() and are never put in state. On page refresh
 * or logout the key is lost and the user must re-enter their master password.
 *
 * Zero-knowledge flow (KB architecture/secrets-management.md):
 * 1. setup: client generates salt, derives the PBKDF2 key, computes the
 *    verifier (computeVerifier) and POSTs {verifier, key_salt, key_version}
 *    to /secrets/setup. The API stores only argon2id(verifier).
 * 2. unlock: GET /secrets/key returns { data: { key_salt, key_version } }, or
 *    404 if not set up. Client derives the key, computes the verifier and
 *    POSTs {verifier, key_version} to /secrets/verify: 200 match / 422
 *    INVALID_MASTER_KEY wrong password / 429 too many wrong attempts (#4446).
 *    A 401 only ever means the session ended (normal /login redirect). Households set up before migration 051 are upgraded by the API
 *    on this first successful verify; the UI sends the same value either way.
 * 3. lock: clear CryptoKey from memory, set isUnlocked=false.
 *
 * All calls go through apiFetch, i.e. the same-origin BFF (/bff/secrets/...).
 */

import { create } from "zustand";
import { apiFetch, ApiError } from "@/lib/api";
import { deriveKeyAndVerifier, generateSalt } from "@/lib/crypto";

/** Default key version — bump when rotating keys. */
export const DEFAULT_KEY_VERSION = 1;

/** Response body for GET /api/v1/secrets/key (wrapped in { data: ... }). */
export interface KeyInfoResponse {
  /** base64-encoded PBKDF2 salt. */
  key_salt: string;
  key_version: number;
}

/** Body of POST /api/v1/secrets/setup. Never contains the key or password. */
export interface SetupKeyRequest {
  verifier: string;
  key_salt: string;
  key_version: number;
}

/** Body of POST /api/v1/secrets/verify. Never contains the key or password. */
export interface VerifyKeyRequest {
  verifier: string;
  key_version: number;
}

/**
 * Build the /secrets/setup body. Sends `verifier` (the API still accepts the
 * pre-051 name `key_hash` as an alias, but we no longer send it).
 */
export function buildSetupRequest(
  verifier: string,
  keySalt: string,
  keyVersion: number = DEFAULT_KEY_VERSION,
): SetupKeyRequest {
  return { verifier, key_salt: keySalt, key_version: keyVersion };
}

/** Build the /secrets/verify body. */
export function buildVerifyRequest(
  verifier: string,
  keyVersion: number = DEFAULT_KEY_VERSION,
): VerifyKeyRequest {
  return { verifier, key_version: keyVersion };
}

/**
 * GET /api/v1/secrets/key. Returns the stored salt and version, or null when
 * the household has no master password yet (404). Other errors are thrown.
 */
export async function fetchKeyInfo(): Promise<KeyInfoResponse | null> {
  try {
    const res = await apiFetch<{ data: KeyInfoResponse }>("/api/v1/secrets/key", {
      method: "GET",
    });
    return res.data;
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) return null;
    throw err;
  }
}

/**
 * Does the household still need to set up a master password? True only on a
 * 404 from GET /secrets/key. Any other failure (network, 5xx) returns false so
 * the caller shows the unlock flow, whose own error handling surfaces it,
 * rather than offering a setup that would overwrite nothing and fail.
 */
export async function probeNeedsSetup(): Promise<boolean> {
  try {
    return (await fetchKeyInfo()) === null;
  } catch {
    return false;
  }
}

interface SecretsState {
  /** True when cryptoKey is loaded and ready for encrypt/decrypt. */
  isUnlocked: boolean;
  /** The derived AES-GCM key — never leaves memory. */
  cryptoKey: CryptoKey | null;
  /** Tracks which key version is loaded. */
  keyVersion: number;
  /** True while setup/unlock network calls are in flight. */
  isProcessing: boolean;
  /** Last error message (cleared on next action). */
  error: string | null;
}

interface SecretsActions {
  /**
   * First-time master password setup.
   * Generates a fresh salt, derives the key, computes the verifier and POSTs
   * {verifier, key_salt, key_version} to /api/v1/secrets/setup.
   * Only succeeds if no key exists yet for this household (409 otherwise).
   */
  setup: (password: string) => Promise<void>;
  /**
   * Unlock an already-set-up secrets vault.
   * Fetches the stored salt, derives the key, sends the verifier to /verify.
   * On 200, loads the CryptoKey. A wrong password is 422 INVALID_MASTER_KEY,
   * which becomes "Incorrect master password." (never 401 — a 401 is a lost
   * session and takes the normal /login redirect, #4445/#4446). A 429 means
   * too many wrong attempts. If no key is set up, throws a not-set-up error.
   */
  unlock: (password: string) => Promise<void>;
  /** Clear the CryptoKey from memory and lock the vault. */
  lock: () => void;
  /** Clear any error message. */
  clearError: () => void;
  /**
   * Set an error message (e.g. client-side validation failures from a
   * component). Pass `null` to clear. This is the single write-path for
   * user-facing error display — `setup`/`unlock` set `error` internally on
   * failure, and components call this for validation errors so there is one
   * source of truth for the error shown in the UI.
   */
  setError: (error: string | null) => void;
}

export type SecretsStore = SecretsState & SecretsActions;

const NOT_SET_UP = "Secrets have not been set up. Set a master password first.";

export const useSecretsStore = create<SecretsStore>()((set) => ({
  isUnlocked: false,
  cryptoKey: null,
  keyVersion: DEFAULT_KEY_VERSION,
  isProcessing: false,
  error: null,

  setup: async (password: string) => {
    set({ isProcessing: true, error: null });
    try {
      const salt = generateSalt();
      // One KDF pass: the same PBKDF2 bytes give the verifier and a
      // NON-EXTRACTABLE AES key, so nothing in the store can be exported.
      const { key, verifier } = await deriveKeyAndVerifier(password, salt);

      await apiFetch("/api/v1/secrets/setup", {
        method: "POST",
        body: buildSetupRequest(verifier, salt, DEFAULT_KEY_VERSION),
      });

      set({
        cryptoKey: key,
        keyVersion: DEFAULT_KEY_VERSION,
        isUnlocked: true,
        isProcessing: false,
      });
    } catch (err) {
      set({
        isProcessing: false,
        error: err instanceof Error ? err.message : "Setup failed",
      });
      throw err;
    }
  },

  unlock: async (password: string) => {
    set({ isProcessing: true, error: null });
    try {
      // 1. Retrieve the stored salt (null = no key set up yet).
      const keyInfo = await fetchKeyInfo();
      if (!keyInfo) throw new Error(NOT_SET_UP);
      if (!keyInfo.key_salt) {
        throw new Error("Server did not return a key salt.");
      }
      const keyVersion = keyInfo.key_version || DEFAULT_KEY_VERSION;

      // 2. Derive the key locally from the password + stored salt. One pass
      //    gives the verifier and a NON-EXTRACTABLE key; the verifier value is
      //    unchanged, so the stored argon2id(verifier) still matches.
      const { key, verifier } = await deriveKeyAndVerifier(password, keyInfo.key_salt);

      // 3. Verify. 200 = correct password, 422 = wrong password, 429 = the
      //    household is locked out for a while. A 401 is a lost session and
      //    takes apiFetch's normal /login redirect.
      try {
        await apiFetch("/api/v1/secrets/verify", {
          method: "POST",
          body: buildVerifyRequest(verifier, keyVersion),
        });
      } catch (err) {
        if (err instanceof ApiError && err.status === 422) {
          throw new Error("Incorrect master password.");
        }
        if (err instanceof ApiError && err.status === 429) {
          throw new Error("Too many wrong master passwords. Wait a few minutes and try again.");
        }
        throw err;
      }

      set({
        cryptoKey: key,
        keyVersion,
        isUnlocked: true,
        isProcessing: false,
      });
    } catch (err) {
      set({
        isProcessing: false,
        error: err instanceof Error ? err.message : "Unlock failed",
      });
      throw err;
    }
  },

  lock: () =>
    set({
      cryptoKey: null,
      isUnlocked: false,
      keyVersion: DEFAULT_KEY_VERSION,
      error: null,
    }),

  clearError: () => set({ error: null }),

  setError: (error: string | null) => set({ error }),
}));
