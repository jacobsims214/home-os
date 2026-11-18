"use client";

/**
 * Settings -> Agent tokens (#4580): the tokens a person gives their own AI agent.
 *
 * Per the approved settings mockup (artifact #103 v1, "Agent tokens"):
 *   - a create form (name, access, expiry), a list of active tokens with the
 *     last time each was used, and a collapsed "Revoked" group;
 *   - the token itself is shown ONCE, in the dialog right after creation, with
 *     a copy button and the warning that closing it loses the value;
 *   - revoking is a confirmable action, because a revoke cannot be undone (a
 *     new token is a different secret).
 *
 * WHAT THIS FILE DOES NOT DO: it changes no token policy. The scopes (`read`,
 * `write`), who may create which (viewer = read only; housesitter and vendor
 * none), and the once-only return are the API's
 * (apps/api/internal/agenttoken, apps/api/internal/auth/roles.go
 * ResAgentTokens). The form disables what the API would refuse and says why,
 * so nobody submits into a 403 — it never widens what the API allows.
 *
 * THE SECRET: `POST /api/v1/agent-tokens` is the only response that carries the
 * plaintext (Cache-Control: no-store). It is held in this component's state,
 * cleared when the dialog closes, and deliberately kept out of the TanStack
 * query cache (the mutation is reset as soon as the value is copied into state)
 * and out of localStorage. Every list, and every render after the dialog
 * closes, shows `token_prefix` only — see maskedToken().
 */

import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Alert,
  Button,
  CopyButton,
  Skeleton,
  Stack,
  Text,
  TextInput,
} from "@mantine/core";
import { notifications } from "@mantine/notifications";
import {
  IconAlertTriangle,
  IconCheck,
  IconCopy,
  IconEye,
  IconInfoCircle,
  IconPlus,
  IconRobot,
} from "@tabler/icons-react";
import { apiFetch, ApiError } from "@/lib/api";
import { useNavRole } from "@/components/shared/use-nav-role";
import { ResponsiveModal, ResponsiveModalActions } from "@/components/ui";
import { SectionHeader, SettingsCard } from "./SettingsFrame";
import classes from "./settings.module.css";
import {
  type AgentToken,
  canCreateAgentTokens,
  canWriteAgentTokens,
  expiryFromDays,
  expiryLabel,
  expiresSoon,
  lastUsedLabel,
  maskedToken,
  revokedLabel,
  scopeLabel,
  scopesFor,
  splitTokens,
} from "./agent-tokens";

/** The token list key: the caller's own tokens, per family (the BFF scopes it). */
const TOKEN_QUERY_KEY = ["agent-tokens", "list"] as const;

/** The expiry choices the form offers; null is the API's "no expires_at". */
const EXPIRY_CHOICES = [
  { value: "90", label: "In 90 days" },
  { value: "30", label: "In 30 days" },
  { value: "365", label: "In 1 year" },
  { value: "never", label: "Never" },
] as const;

const STROKE = 1.75;

/** The API's own sentence for a refused create, or a plain fallback. */
function createFailureMessage(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return "That didn't work. Nothing was created — check your connection and try again.";
  }
  if (error.status === 403) {
    return "Your role in this family can't create that kind of token. Nothing was created.";
  }
  const body = error.body as { error?: { message?: unknown } } | undefined;
  const message = body?.error?.message;
  if (typeof message === "string" && message.trim() !== "") return message;
  return "That didn't work. Nothing was created — try again.";
}

/** One active token: name, the masked prefix, what it can do, and Revoke. */
function TokenRow({ token, onRevoke }: { token: AgentToken; onRevoke: (token: AgentToken) => void }) {
  const soon = expiresSoon(token.expires_at);
  return (
    <li className={classes.tokenRow} data-agent-token={token.id}>
      <span className={classes.tokenIcon} aria-hidden>
        <IconRobot size={20} stroke={STROKE} />
      </span>
      <div className={classes.tokenMain}>
        <div className={classes.tokenName}>{token.name}</div>
        {/* The prefix is the only part of a token the API returns after
            creation, so it is the only part a list can ever draw. */}
        <code className={classes.tokenPrefix} data-token-prefix>
          {maskedToken(token.token_prefix)}
        </code>
        <div className={classes.tokenMeta}>
          <span className={classes.tokenTag}>{scopeLabel(token.scopes)}</span>
          <span>{lastUsedLabel(token.last_used_at)}</span>
          <span className={soon ? classes.tokenTagWarn : undefined}>{expiryLabel(token.expires_at)}</span>
        </div>
      </div>
      <Button
        variant="subtle"
        color="red"
        size="compact-sm"
        className={classes.tokenRevoke}
        onClick={() => onRevoke(token)}
        aria-label={`Revoke ${token.name}`}
      >
        Revoke
      </Button>
    </li>
  );
}

/** One revoked token: it stays visible so the name means something later. */
function RevokedRow({ token }: { token: AgentToken }) {
  return (
    <li className={classes.tokenRow} data-agent-token-revoked={token.id}>
      <span className={classes.tokenIcon} aria-hidden>
        <IconRobot size={20} stroke={STROKE} />
      </span>
      <div className={classes.tokenMain}>
        <div className={classes.tokenName}>{token.name}</div>
        <code className={classes.tokenPrefix}>{maskedToken(token.token_prefix)}</code>
        <div className={classes.tokenMeta}>
          <span>{revokedLabel(token.revoked_at)}</span>
          {token.last_used_at && <span>{lastUsedLabel(token.last_used_at)}</span>}
        </div>
      </div>
    </li>
  );
}

/**
 * The create form. Local state only, reset every time it opens, so a cancelled
 * draft never lingers and no name or choice is carried into the next token.
 */
function CreateTokenModal({
  opened,
  role,
  onClose,
  onCreated,
}: {
  opened: boolean;
  role: string | null;
  onClose: () => void;
  /** Hands the one-time plaintext to the parent, which shows it once. */
  onCreated: (created: { token: string; name: string; scopes: string[]; expires_at: string | null }) => void;
}) {
  const queryClient = useQueryClient();
  const canWrite = canWriteAgentTokens(role);
  const [name, setName] = useState("");
  const [write, setWrite] = useState(false);
  const [expiry, setExpiry] = useState<string>("90");
  const [error, setError] = useState<string | null>(null);

  const reset = () => {
    setName("");
    setWrite(false);
    setExpiry("90");
    setError(null);
    create.reset();
  };

  const close = () => {
    reset();
    onClose();
  };

  const create = useMutation({
    mutationFn: (body: { name: string; scopes: string[]; expires_at?: string }) =>
      apiFetch<{ data: AgentToken & { token: string } }>("/api/v1/agent-tokens", {
        method: "POST",
        body,
      }),
    onSuccess: (res) => {
      const created = res?.data;
      if (!created?.token) {
        setError("The token was created but its value didn't come back. Revoke it and create another.");
        return;
      }
      // Hand the secret to the parent's state, then drop it from the mutation
      // cache: one place holds it, and it lives only as long as the dialog.
      onCreated({
        token: created.token,
        name: created.name,
        scopes: created.scopes,
        expires_at: created.expires_at,
      });
      queryClient.invalidateQueries({ queryKey: TOKEN_QUERY_KEY });
      create.reset();
      setName("");
      setWrite(false);
      setExpiry("90");
      setError(null);
      onClose();
    },
    onError: (err: unknown) => setError(createFailureMessage(err)),
  });

  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    const trimmed = name.trim();
    if (trimmed === "") {
      setError("Give the token a name, so you can tell your tokens apart later.");
      return;
    }
    if (trimmed.length > 255) {
      setError("Use at most 255 characters.");
      return;
    }
    setError(null);
    const days = expiry === "never" ? null : Number(expiry);
    create.mutate({
      name: trimmed,
      scopes: scopesFor(write && canWrite),
      expires_at: expiryFromDays(days),
    });
  };

  return (
    <ResponsiveModal opened={opened} onClose={close} title="Create agent token">
      <form onSubmit={submit} noValidate>
        <Stack gap="md">
          <TextInput
            label="Name"
            required
            data-autofocus
            value={name}
            placeholder="Moltis on the home server"
            description="So you can tell your tokens apart: the agent and the device."
            onChange={(event) => {
              setName(event.currentTarget.value);
              setError(null);
            }}
            styles={{ input: { fontSize: 16 } }}
          />

          <fieldset className={classes.choiceSet}>
            <legend className={classes.choiceLegend}>Access</legend>
            <button
              type="button"
              className={classes.choiceCard}
              role="radio"
              aria-checked={!write || !canWrite}
              aria-disabled={false}
              onClick={() => setWrite(false)}
            >
              <span className={classes.choiceDot} aria-hidden />
              <span className={classes.choiceText}>
                <b>Read only</b>
                <span>Look things up: calendar, lists, bills, shared logins</span>
              </span>
            </button>
            <button
              type="button"
              className={classes.choiceCard}
              role="radio"
              aria-checked={write && canWrite}
              aria-disabled={!canWrite}
              disabled={!canWrite}
              onClick={() => setWrite(true)}
            >
              <span className={classes.choiceDot} aria-hidden />
              <span className={classes.choiceText}>
                <b>Read &amp; write</b>
                <span>
                  {canWrite
                    ? "Also add events, check off lists, update records"
                    : "Your role can only create read-only tokens"}
                </span>
              </span>
            </button>
          </fieldset>

          <div className={classes.choiceSet} role="radiogroup" aria-label="Expires">
            <span className={classes.choiceLegend} aria-hidden>
              Expires
            </span>
            <div className={classes.expiryRow}>
              {EXPIRY_CHOICES.map((choice) => (
                <button
                  key={choice.value}
                  type="button"
                  role="radio"
                  aria-checked={expiry === choice.value}
                  className={classes.expiryChip}
                  onClick={() => setExpiry(choice.value)}
                >
                  {choice.label}
                </button>
              ))}
            </div>
          </div>

          <Alert color="blue" variant="light" icon={<IconInfoCircle size={16} />}>
            The token acts as you, in this family only. If your role changes, the token follows it.
          </Alert>

          {error && (
            <Alert color="red" variant="light" icon={<IconAlertTriangle size={16} />} role="alert">
              {error}
            </Alert>
          )}
        </Stack>

        <ResponsiveModalActions>
          <Button variant="default" type="button" onClick={close}>
            Cancel
          </Button>
          <Button type="submit" loading={create.isPending}>
            Create token
          </Button>
        </ResponsiveModalActions>
      </form>
    </ResponsiveModal>
  );
}

/** The one chance to copy the token. Closing it drops the value for good. */
function NewTokenModal({
  created,
  origin,
  onClose,
}: {
  created: { token: string; name: string; scopes: string[]; expires_at: string | null } | null;
  /** The hub's own address, read after mount (see the section below). */
  origin: string;
  onClose: () => void;
}) {
  return (
    <ResponsiveModal
      opened={created != null}
      onClose={onClose}
      title="Copy your token"
      closeOnBack={false}
      closeLabel="Close and discard the token"
    >
      {created && (
        <>
          <div className={classes.secretBox} role="status">
            <div className={classes.secretHead}>
              <IconEye size={18} stroke={STROKE} aria-hidden />
              This is the only time you&apos;ll see it.
            </div>
            <div className={classes.copyRow}>
              <code className={classes.secretValue} data-new-token>
                {created.token}
              </code>
              <CopyButton value={created.token} timeout={2000}>
                {({ copied, copy }) => (
                  <Button
                    onClick={copy}
                    leftSection={copied ? <IconCheck size={16} /> : <IconCopy size={16} />}
                    aria-label={copied ? "Copied the token" : "Copy the token"}
                    className={classes.copyButton}
                  >
                    {copied ? "Copied" : "Copy"}
                  </Button>
                )}
              </CopyButton>
            </div>
            <p className={classes.secretHelp}>
              Paste it into your agent now. If you lose it, revoke it here and create a new one.
            </p>
          </div>

          <div className={classes.tokenSummary}>
            <b>{created.name}</b>
            <span>
              {scopeLabel(created.scopes)} · {expiryLabel(created.expires_at)}
            </span>
          </div>

          <Text size="sm" fw={600} mt="md" mb={6}>
            Example agent config
          </Text>
          <pre className={classes.codeBlock}>
            {`{
  "url": "${origin}/mcp",
  "headers": { "Authorization": "Bearer ${created.token.slice(0, 10)}…" }
}`}
          </pre>
        </>
      )}

      <ResponsiveModalActions>
        <Button onClick={onClose}>I&apos;ve copied it</Button>
      </ResponsiveModalActions>
    </ResponsiveModal>
  );
}

export default function AgentTokensSection() {
  const role = useNavRole();
  const queryClient = useQueryClient();
  const canCreate = canCreateAgentTokens(role);

  const [createOpen, setCreateOpen] = useState(false);
  /**
   * The one-time plaintext, in component state only. Never the query cache
   * (the create mutation is reset), never localStorage: a page reload is
   * supposed to lose it, because the server kept only the hash.
   */
  const [created, setCreated] = useState<{
    token: string;
    name: string;
    scopes: string[];
    expires_at: string | null;
  } | null>(null);
  const [revoking, setRevoking] = useState<AgentToken | null>(null);
  /**
   * The hub's address for the MCP example. Read after mount, never during
   * render: the server has no window, and rendering it inline would make the
   * first client paint differ from the server's HTML (a hydration mismatch).
   */
  const [origin, setOrigin] = useState("");
  useEffect(() => {
    setOrigin(window.location.origin);
  }, []);

  const { data, isLoading, isError, refetch, isRefetching } = useQuery({
    queryKey: TOKEN_QUERY_KEY,
    queryFn: () => apiFetch<{ data: AgentToken[] }>("/api/v1/agent-tokens"),
  });

  const tokens = useMemo(() => data?.data ?? [], [data]);
  const { active, revoked } = useMemo(() => splitTokens(tokens), [tokens]);

  const revoke = useMutation({
    mutationFn: (token: AgentToken) =>
      apiFetch<void>(`/api/v1/agent-tokens/${token.id}`, { method: "DELETE" }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: TOKEN_QUERY_KEY });
      notifications.show({ message: "Token revoked. It stops working right away.", color: "green" });
      setRevoking(null);
    },
    onError: (error: unknown) => {
      const message =
        error instanceof ApiError && error.status === 404
          ? "That token is already gone. Reloading the list."
          : "That didn't work. The token is still active — try again.";
      notifications.show({ message, color: "red" });
      if (error instanceof ApiError && error.status === 404) {
        queryClient.invalidateQueries({ queryKey: TOKEN_QUERY_KEY });
        setRevoking(null);
      }
    },
  });

  return (
    <>
      <SectionHeader
        title="Agent tokens"
        description="Let your own AI agent, like Moltis or Claude, read or update this family hub. Each token is yours alone, works only in this family, and can be revoked at any time."
      />

      {isLoading && (
        <div aria-busy="true" aria-label="Loading agent tokens">
          <div className={classes.card}>
            <div className={classes.cardBody}>
              <Skeleton height={16} width="45%" radius="sm" />
              <Skeleton height={12} width="70%" radius="sm" mt={10} />
            </div>
          </div>
        </div>
      )}

      {isError && (
        <Alert
          color="orange"
          variant="light"
          icon={<IconAlertTriangle size={16} />}
          role="alert"
          title="Couldn't load your tokens"
          mb="md"
        >
          <Stack gap="sm" align="flex-start">
            <Text size="sm">Nothing was changed. Check your connection and try again.</Text>
            <Button variant="default" onClick={() => refetch()} loading={isRefetching}>
              Try again
            </Button>
          </Stack>
        </Alert>
      )}

      {!isLoading && !isError && tokens.length === 0 && (
        <div className={`${classes.card} ${classes.emptyCard}`} data-agent-tokens-empty>
          <span className={classes.emptyArt} aria-hidden>
            <IconRobot size={28} stroke={STROKE} />
          </span>
          <h3 className={classes.emptyTitle}>No agent tokens yet</h3>
          <p className={classes.emptyText}>
            Create one and paste it into your agent&apos;s settings. Your agent then sees what you can
            see in this family, nothing more.
          </p>
          {canCreate ? (
            <Button onClick={() => setCreateOpen(true)} leftSection={<IconPlus size={16} />} className={classes.tapTarget}>
              Create token
            </Button>
          ) : (
            <Text size="sm" c="var(--ink-3)">
              Your role in this family can&apos;t create agent tokens. An owner or a family manager
              can change your role if you need one.
            </Text>
          )}
        </div>
      )}

      {!isLoading && !isError && tokens.length > 0 && (
        <>
          <div className={classes.sectionHeadingRow}>
            <h3 className={classes.sectionHeading}>
              Active
              {active.length > 0 && <span className={classes.sectionCount}>{active.length}</span>}
            </h3>
            {canCreate && (
              <Button
                size="compact-sm"
                onClick={() => setCreateOpen(true)}
                leftSection={<IconPlus size={16} />}
                className={classes.tapTarget}
              >
                Create token
              </Button>
            )}
          </div>

          {active.length === 0 ? (
            <div className={`${classes.card} ${classes.emptyCard}`} data-agent-tokens-none-active>
              <p className={classes.emptyText}>
                No active tokens. Create one when you want your agent to have access again.
              </p>
            </div>
          ) : (
            <ul className={classes.tokenList} aria-label="Active agent tokens">
              {active.map((token) => (
                <TokenRow key={token.id} token={token} onRevoke={setRevoking} />
              ))}
            </ul>
          )}

          {revoked.length > 0 && (
            <details className={classes.revokedGroup} data-agent-tokens-revoked>
              <summary className={classes.revokedSummary}>
                Revoked<span className={classes.sectionCount}>{revoked.length}</span>
              </summary>
              <ul className={classes.tokenList}>
                {revoked.map((token) => (
                  <RevokedRow key={token.id} token={token} />
                ))}
              </ul>
            </details>
          )}
        </>
      )}

      <SettingsCard
        title="Connect your agent"
        description="Give your agent this address and a token. It speaks MCP."
      >
        <Text size="sm" c="var(--ink-2)">
          The address is <code>{origin ? `${origin}/mcp` : "/mcp"}</code> — your agent sends the token
          as <code>Authorization: Bearer hos_…</code>. Everything it reads or changes shows up in
          Activity under your name.
        </Text>
      </SettingsCard>

      <CreateTokenModal
        opened={createOpen}
        role={role}
        onClose={() => setCreateOpen(false)}
        onCreated={setCreated}
      />

      <NewTokenModal created={created} origin={origin} onClose={() => setCreated(null)} />

      <ResponsiveModal
        opened={revoking != null}
        onClose={() => setRevoking(null)}
        title={revoking ? `Revoke ${revoking.name}?` : "Revoke token?"}
        size="sm"
      >
        <Text size="sm" c="var(--ink-2)">
          Your agent stops working with this token right away. This can&apos;t be undone — if you need
          access again, create a new token and paste it into your agent.
        </Text>
        <ResponsiveModalActions>
          <Button variant="default" onClick={() => setRevoking(null)} disabled={revoke.isPending}>
            Keep token
          </Button>
          <Button
            color="red"
            onClick={() => revoking && revoke.mutate(revoking)}
            loading={revoke.isPending}
          >
            Revoke token
          </Button>
        </ResponsiveModalActions>
      </ResponsiveModal>
    </>
  );
}
