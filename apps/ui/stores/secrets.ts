/**
 * Secrets Zustand store — manages the in-memory encryption key lifecycle.
 *
 * The derived CryptoKey is held ONLY in memory. It is never persisted to
 * localStorage, sessionStorage, or cookies. On page refresh or logout the
 * key is lost and the user must re-enter their master password.
 *
 * Zero-knowledge flow (see architecture/secrets-manager-research.md):
 * 1. setup: client generates salt, derives PBKDF2 key, hashes key, sends
 *    {key_hash, key_salt, key_version} to API. API stores them. Client keeps
 *    the CryptoKey in memory.
 * 2. unlock: client asks API for the stored key_salt (GET /key — API
 *    returns { data: { key_salt, key_version } } if a key exists, 404 if not
 *    set up). Client derives key, hashes it, POSTs /verify with the hash. API
 *    compares hashes → 200 match / 401 wrong password.
 * 3. lock: clear CryptoKey from memory, set isUnlocked=false.
 */

import { create } from "zustand";
import { apiFetch, ApiError } from "@/lib/api";
import {
  deriveKey,
  generateSalt,
  hashKey,
} from "@/lib/crypto";

/** Default key version — bump when rotating keys. */
const DEFAULT_KEY_VERSION = 1;

/** Response body for GET /api/v1/secrets/key (wrapped in { data: ... }). */
interface KeyInfoResponse {
  /** base64-encoded PBKDF2 salt. */
  key_salt: string;
  key_version: number;
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
   * Generates a fresh salt, derives the key, hashes it, and POSTs
   * {key_hash, key_salt, key_version} to /api/v1/secrets/setup.
   * Only succeeds if no key exists yet for this household.
   */
  setup: (password: string) => Promise<void>;
  /**
   * Unlock an already-set-up secrets vault.
   * Fetches the stored salt, derives the key, verifies the hash against
   * the API. On 200, loads the CryptoKey. On 401, throws a wrong-password
   * error. On 404, throws a not-set-up error.
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
      const key = await deriveKey(password, salt);
      const hash = await hashKey(key);

      await apiFetch("/api/v1/secrets/setup", {
        method: "POST",
        body: {
          key_hash: hash,
          key_salt: salt,
          key_version: DEFAULT_KEY_VERSION,
        },
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
      // 1. Retrieve stored salt via GET /key. API returns
      //    { data: { key_salt, key_version } } when a key exists,
      //    404 when no key has been set up yet.
      let keyInfo: KeyInfoResponse;
      try {
        const res = await apiFetch<{ data: KeyInfoResponse }>(
          "/api/v1/secrets/key",
          { method: "GET" },
        );
        keyInfo = res.data;
      } catch (err) {
        if (err instanceof ApiError && err.status === 404) {
          throw new Error(
            "Secrets have not been set up. Set a master password first.",
          );
        }
        throw err;
      }

      const salt = keyInfo.key_salt;
      if (!salt) {
        throw new Error("Server did not return a key salt.");
      }

      // 2. Derive the key locally from the password + stored salt.
      const key = await deriveKey(password, salt);
      const hash = await hashKey(key);

      // 3. Verify the hash against the API. 200 = correct password,
      //    401 = wrong password.
      try {
        await apiFetch("/api/v1/secrets/verify", {
          method: "POST",
          body: {
            key_hash: hash,
            key_version: DEFAULT_KEY_VERSION,
          },
        });
      } catch (err) {
        if (err instanceof ApiError && err.status === 401) {
          throw new Error("Incorrect master password.");
        }
        throw err;
      }

      set({
        cryptoKey: key,
        keyVersion: DEFAULT_KEY_VERSION,
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
