"use client";

/**
 * CookMode (#4567): full-screen, hands-messy cooking view of one recipe.
 *
 * Built to the approved mockup (#106 v3, "Cook mode" state): a full-screen
 * dialog over the shell (like Lists' store mode), the current step expanded,
 * ingredients collapsible at the top, tap a step to mark it done, and a
 * "Screen on" toggle.
 *
 * Readability on a phone is the whole point (acceptance criterion: "cook mode
 * keeps the screen readable"):
 *  - steps are 17 px at 1.55 line-height (the mockup's .cstep p), not 14 px;
 *  - the current step is outlined with --accent and a 3 px --accent-soft ring,
 *    so "where am I" is answerable from arm's length;
 *  - a completed step is dimmed AND struck through, so it is not colour-only;
 *  - every step is a 44 px+ tap target, and the whole row is the target.
 *
 * Screen Wake Lock: requested on open, released on close, and re-requested when
 * the tab becomes visible again (the browser drops the lock on hide). It is
 * silently skipped where unsupported (Safari < 16.4, or a non-secure origin) —
 * the toggle then says so instead of pretending. The lock is a nicety: the
 * screen dimming mid-recipe must never break cooking.
 *
 * Progress is NOT persisted: the API has no "cooking session" concept, and a
 * step marked done is only true for this cooking of this recipe.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button, Checkbox, Collapse, Group, Progress, Stack, Text, UnstyledButton } from "@mantine/core";
import { IconChevronDown, IconChevronLeft, IconDeviceMobile, IconCheck } from "@tabler/icons-react";
import { formatQuantity, parseSteps, type Ingredient } from "@/lib/recipes/ingredients";

const ICON_STROKE = 1.75;

/** The subset of the Wake Lock API this uses (not in TS's DOM lib yet). */
interface WakeLockSentinelLike {
  released: boolean;
  release: () => Promise<void>;
  addEventListener: (type: "release", listener: () => void) => void;
}
interface WakeLockLike {
  request: (type: "screen") => Promise<WakeLockSentinelLike>;
}

function wakeLockApi(): WakeLockLike | null {
  if (typeof navigator === "undefined") return null;
  const api = (navigator as Navigator & { wakeLock?: WakeLockLike }).wakeLock;
  return api && typeof api.request === "function" ? api : null;
}

interface Props {
  opened: boolean;
  onClose: () => void;
  title: string;
  ingredients: readonly Ingredient[];
  /** The recipe's steps as one text blob (one per line), as the API stores them. */
  steps?: string | null;
}

export default function CookMode({ opened, onClose, title, ingredients, steps }: Props) {
  const stepList = useMemo(() => parseSteps(steps ?? ""), [steps]);
  const [done, setDone] = useState<Set<number>>(() => new Set());
  const [ingredientsOpen, setIngredientsOpen] = useState(false);
  const [screenOn, setScreenOn] = useState(false);
  const [wakeSupported, setWakeSupported] = useState(true);
  const sentinel = useRef<WakeLockSentinelLike | null>(null);
  const closeRef = useRef<HTMLButtonElement>(null);

  // Current step = the first one not marked done. Everything after it is
  // "future" and dimmed; everything before is done.
  const currentIndex = stepList.findIndex((_, i) => !done.has(i));
  const allDone = stepList.length > 0 && currentIndex === -1;
  const progress = stepList.length === 0 ? 0 : (done.size / stepList.length) * 100;

  const release = useCallback(async () => {
    const s = sentinel.current;
    sentinel.current = null;
    if (s && !s.released) {
      try {
        await s.release();
      } catch {
        // Releasing an already-released lock throws in some engines; harmless.
      }
    }
    setScreenOn(false);
  }, []);

  const acquire = useCallback(async () => {
    const api = wakeLockApi();
    if (!api) {
      setWakeSupported(false);
      return;
    }
    try {
      const s = await api.request("screen");
      sentinel.current = s;
      setScreenOn(true);
      s.addEventListener("release", () => setScreenOn(false));
    } catch {
      // NotAllowedError (low battery, backgrounded) or a permissions policy:
      // cooking still works, so say nothing louder than the toggle state.
      setScreenOn(false);
    }
  }, []);

  // Reset the cooking session each time the dialog opens.
  useEffect(() => {
    if (!opened) return;
    setDone(new Set());
    setIngredientsOpen(false);
    // Focus the close button so Escape/back and a screen reader land sensibly.
    const t = window.setTimeout(() => closeRef.current?.focus({ preventScroll: true }), 40);
    return () => window.clearTimeout(t);
  }, [opened]);

  // Wake lock lifecycle: on open, off on close, and re-acquired when the tab
  // comes back (the browser releases it when the page is hidden).
  useEffect(() => {
    if (!opened) {
      void release();
      return;
    }
    void acquire();
    const onVisible = () => {
      if (document.visibilityState === "visible" && sentinel.current === null) void acquire();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      document.removeEventListener("visibilitychange", onVisible);
      void release();
    };
  }, [opened, acquire, release]);

  // Escape closes, like every other dialog in the app.
  useEffect(() => {
    if (!opened) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [opened, onClose]);

  const toggleStep = (index: number) => {
    setDone((prev) => {
      const next = new Set(prev);
      if (next.has(index)) next.delete(index);
      else next.add(index);
      return next;
    });
  };

  if (!opened) return null;

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="cook-title"
      data-cook-mode
      style={{
        position: "fixed",
        inset: 0,
        zIndex: 400,
        display: "flex",
        flexDirection: "column",
        background: "var(--bg)",
        // Full-screen over the shell, including the phone tab bar.
        paddingTop: "env(safe-area-inset-top, 0px)",
        paddingBottom: "env(safe-area-inset-bottom, 0px)",
      }}
    >
      <header
        style={{
          flex: "none",
          background: "var(--surface)",
          borderBottom: "1px solid var(--line)",
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: 4, minHeight: 60, padding: "0 8px" }}>
          <Button
            ref={closeRef}
            variant="subtle"
            onClick={onClose}
            leftSection={<IconChevronLeft size={20} stroke={ICON_STROKE} />}
            className="max-md:min-h-11"
            data-cook-close
          >
            Done
          </Button>
          <div style={{ flex: 1, minWidth: 0, textAlign: "center" }}>
            <Text
              id="cook-title"
              fw={600}
              size="md"
              style={{ whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" }}
            >
              {title}
            </Text>
            <Text size="xs" style={{ color: "var(--ink-3)" }} data-cook-counter>
              {stepList.length === 0
                ? "No steps saved"
                : allDone
                  ? `All ${stepList.length} steps done`
                  : `Step ${currentIndex + 1} of ${stepList.length}`}
            </Text>
          </div>
          <Button
            variant={screenOn ? "light" : "subtle"}
            color={screenOn ? "teal" : "gray"}
            onClick={() => (screenOn ? void release() : void acquire())}
            leftSection={<IconDeviceMobile size={16} stroke={ICON_STROKE} />}
            aria-pressed={screenOn}
            disabled={!wakeSupported}
            title={
              wakeSupported
                ? "Keep the screen awake while you cook"
                : "This browser can't keep the screen awake; set Auto-Lock longer in Settings"
            }
            className="max-md:min-h-11"
            data-cook-wake
          >
            {screenOn ? "Screen on" : wakeSupported ? "Keep on" : "No"}
          </Button>
        </div>
        {stepList.length > 0 && (
          <Progress
            value={progress}
            size="xs"
            radius={0}
            color="teal"
            aria-label={`${done.size} of ${stepList.length} steps done`}
            data-cook-progress
          />
        )}
      </header>

      <div style={{ flex: 1, overflowY: "auto", padding: 16, width: "100%", maxWidth: 720, margin: "0 auto" }}>
        {/* Ingredients: collapsible and CLOSED by default, so the steps get the
            screen. Opening it does not lose your place in the steps. */}
        {ingredients.length > 0 && (
          <div style={{ marginBottom: 16 }}>
            <UnstyledButton
              onClick={() => setIngredientsOpen((v) => !v)}
              aria-expanded={ingredientsOpen}
              data-cook-ingredients-toggle
              style={{
                display: "flex",
                alignItems: "center",
                gap: 8,
                width: "100%",
                minHeight: 52,
                padding: "0 16px",
                background: "var(--surface)",
                border: "1px solid var(--line)",
                borderRadius: ingredientsOpen ? "var(--r-md) var(--r-md) 0 0" : "var(--r-md)",
                borderBottom: ingredientsOpen ? "0" : "1px solid var(--line)",
              }}
            >
              <Text fw={600} size="md" style={{ flex: 1, textAlign: "left" }}>
                Ingredients · {ingredients.length}
              </Text>
              <IconChevronDown
                size={18}
                stroke={ICON_STROKE}
                style={{
                  color: "var(--ink-3)",
                  transform: ingredientsOpen ? "rotate(180deg)" : "none",
                  transition: "transform .15s",
                }}
                aria-hidden
              />
            </UnstyledButton>
            <Collapse in={ingredientsOpen}>
              <ul
                style={{
                  listStyle: "none",
                  margin: 0,
                  padding: 0,
                  border: "1px solid var(--line)",
                  borderTop: 0,
                  borderRadius: "0 0 var(--r-md) var(--r-md)",
                  background: "var(--surface)",
                  overflow: "hidden",
                }}
              >
                {ingredients.map((ing, i) => (
                  <li
                    key={`${i}-${ing.name}`}
                    style={{
                      display: "grid",
                      gridTemplateColumns: "72px minmax(0, 1fr)",
                      gap: 10,
                      alignItems: "baseline",
                      minHeight: 48,
                      padding: "10px 14px",
                      borderBottom: i === ingredients.length - 1 ? 0 : "1px solid var(--line)",
                      fontSize: 17,
                    }}
                  >
                    <span style={{ fontWeight: 700, color: "var(--ink-2)" }}>{formatQuantity(ing.quantity)}</span>
                    <span style={{ overflowWrap: "anywhere" }}>
                      {ing.unit ? `${ing.unit} ` : ""}
                      {ing.name}
                    </span>
                  </li>
                ))}
              </ul>
            </Collapse>
          </div>
        )}

        {stepList.length === 0 ? (
          <Stack align="center" gap="xs" py="xl">
            <Text fw={600}>No steps saved for this recipe</Text>
            <Text size="sm" style={{ color: "var(--ink-3)", textAlign: "center" }}>
              Add the steps on the recipe page and they&apos;ll show up here, one screen at a time.
            </Text>
          </Stack>
        ) : (
          <Stack gap={10}>
            {stepList.map((step, i) => {
              const isDone = done.has(i);
              const isNow = i === currentIndex;
              return (
                <UnstyledButton
                  key={i}
                  onClick={() => toggleStep(i)}
                  role="checkbox"
                  aria-checked={isDone}
                  aria-label={`Step ${i + 1}: ${step}`}
                  data-cook-step={i}
                  data-step-state={isDone ? "done" : isNow ? "now" : "future"}
                  style={{
                    display: "grid",
                    gridTemplateColumns: "34px minmax(0,1fr) 40px",
                    gap: 14,
                    alignItems: "start",
                    width: "100%",
                    minHeight: 56,
                    padding: 16,
                    textAlign: "left",
                    border: isNow ? "1.5px solid var(--accent)" : "1px solid var(--line)",
                    boxShadow: isNow ? "0 0 0 3px var(--accent-soft)" : undefined,
                    borderRadius: "var(--r-md)",
                    background: "var(--surface)",
                    opacity: isDone ? 0.62 : 1,
                  }}
                >
                  <span
                    aria-hidden
                    style={{
                      display: "grid",
                      placeItems: "center",
                      width: 30,
                      height: 30,
                      borderRadius: "50%",
                      background: isNow ? "var(--accent-fill)" : "var(--surface-2)",
                      color: isNow ? "#fff" : "var(--ink-2)",
                      fontSize: 14,
                      fontWeight: 700,
                    }}
                  >
                    {i + 1}
                  </span>
                  <span
                    style={{
                      fontSize: 17,
                      lineHeight: 1.55,
                      color: isDone || (!isNow && i > currentIndex) ? "var(--ink-3)" : "var(--ink)",
                      textDecoration: isDone ? "line-through" : undefined,
                    }}
                  >
                    {step}
                  </span>
                  <span
                    aria-hidden
                    style={{
                      display: "grid",
                      placeItems: "center",
                      width: 28,
                      height: 28,
                      borderRadius: "50%",
                      border: isDone ? "2px solid var(--accent-fill)" : "2px solid var(--ink-3)",
                      background: isDone ? "var(--accent-fill)" : "transparent",
                      color: isDone ? "#fff" : "transparent",
                    }}
                  >
                    <IconCheck size={18} stroke={2.75} />
                  </span>
                </UnstyledButton>
              );
            })}
          </Stack>
        )}

        {allDone && (
          <Group justify="center" mt="lg">
            <Checkbox checked readOnly label="All done — enjoy it" aria-label="All steps done" />
          </Group>
        )}
      </div>
    </div>
  );
}
