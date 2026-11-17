"use client";

/**
 * AllergyBanner (#4567): what the page says about a recipe's allergies.
 *
 * Three visual registers, chosen by lib/recipes/allergy.ts — never by this
 * component, so every page says the same thing the same way:
 *
 *  - WARN (warn-bg/warn-ink/warn-line): a real match. Shield icon + text,
 *    never colour alone (design guide: no colour-only signalling).
 *  - UNCHECKED (danger border on a warn surface): something could not be
 *    checked. This is #4874's whole point: an allergy string the server could
 *    not read must be VISIBLE, not swallowed. The server's own line is
 *    rendered verbatim — this component never rewords a check, because the
 *    wording is the contract the MCP tools and this page share.
 *  - CALM (surface-2): "checked, nothing matched" or "no allergies saved".
 *
 * The banner is never a modal and never blocks. A meal that is already
 * planned stays planned: someone may be cooking it right now, and a dialog in
 * the middle of dinner is hostile (manager decision, #4519 #13039/#13046).
 * The one hard confirm lives at the add-to-plan step, not here.
 *
 * A housesitter and a viewer see this too: allergies are SAFETY data, and the
 * API lets every reader see them (KB decisions/2026-11-15-role-policy.md).
 */

import { Alert, Group, List, Stack, Text, ThemeIcon } from "@mantine/core";
import { IconInfoCircle, IconShieldCheck, IconShieldExclamation, IconAlertTriangle } from "@tabler/icons-react";
import type { AllergyReport } from "@/lib/recipes/allergy";

const ICON_STROKE = 1.75;

export default function AllergyBanner({ report, compact = false }: { report: AllergyReport; compact?: boolean }) {
  const { state } = report;

  // Nothing to say: the family has no allergies saved and nothing was flagged.
  // (Still shown on a recipe page when the family HAS allergies — that is the
  // "checked-clear" case below.)
  if (state === "none-saved" && report.warnings.length === 0 && report.checks.length === 0) {
    return null;
  }

  const isWarn = state === "warnings" || state === "warnings-and-unchecked";
  const isUnchecked = state === "unchecked" || state === "warnings-and-unchecked" || state === "not-checked";

  const color = isWarn ? "var(--warn-ink)" : isUnchecked ? "var(--danger)" : "var(--accent)";
  const background = isWarn || isUnchecked ? "var(--warn-bg)" : "var(--surface-2)";
  const border = isWarn ? "var(--warn-line)" : isUnchecked ? "var(--danger)" : "var(--line)";
  const Icon = isWarn ? IconShieldExclamation : isUnchecked ? IconAlertTriangle : IconShieldCheck;

  return (
    <Alert
      variant="light"
      radius="token-md"
      p={compact ? "sm" : "md"}
      mb="md"
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      {...({ "data-allergy-state": state } as any)}
      style={{ background, border: `1px solid ${border}`, borderLeft: `4px solid ${color}` }}
      icon={
        <ThemeIcon variant="transparent" color="transparent" style={{ color }} size={22}>
          <Icon size={22} stroke={ICON_STROKE} aria-hidden />
        </ThemeIcon>
      }
      title={
        <Text fw={700} size="sm" style={{ color }} data-allergy-headline>
          {report.headline}
        </Text>
      }
    >
      <Stack gap={8}>
        {report.detail && (
          <Text size="sm" style={{ color: "var(--ink-2)" }}>
            {report.detail}
          </Text>
        )}

        {/* The matches themselves: who, which allergen, what in the recipe. */}
        {report.warnings.length > 0 && (
          <List size="sm" spacing={4} style={{ color: "var(--warn-ink)" }} data-allergy-warnings>
            {report.warnings.map((w, i) => (
              <List.Item key={`${w.person}-${w.allergy}-${w.ingredient}-${i}`}>
                <Text size="sm" component="span" fw={600} style={{ color: "var(--warn-ink)" }}>
                  {w.person || "Someone"}
                </Text>
                {w.allergy ? (
                  <Text size="sm" component="span" style={{ color: "var(--warn-ink)" }}>
                    {" "}
                    is allergic to <strong>{w.allergy}</strong>
                    {w.ingredient ? ` — this recipe has ${w.ingredient}` : ""}
                  </Text>
                ) : (
                  <Text size="sm" component="span" style={{ color: "var(--warn-ink)" }}>
                    {" "}
                    — {w.ingredient}
                  </Text>
                )}
              </List.Item>
            ))}
          </List>
        )}

        {/*
          The server's `checks`, VERBATIM. Never summarised, never hidden behind
          a toggle: an unreadable allergy means the recipe is unverified, and a
          parent scrolling past a collapsed "details" would read the absence of
          warnings as safety (#4874).
        */}
        {report.checks.length > 0 && (
          <Stack gap={4} data-allergy-checks>
            <Group gap={6} wrap="nowrap" align="center">
              <IconInfoCircle size={16} stroke={ICON_STROKE} style={{ color: "var(--danger)" }} aria-hidden />
              <Text size="xs" fw={700} tt="uppercase" style={{ color: "var(--danger)", letterSpacing: ".04em" }}>
                Couldn&rsquo;t be checked
              </Text>
            </Group>
            <List size="sm" spacing={4} icon={<span aria-hidden />} style={{ color: "var(--danger)" }}>
              {report.checks.map((line, i) => (
                <List.Item key={`${i}-${line}`} data-allergy-check={i}>
                  {line}
                </List.Item>
              ))}
            </List>
          </Stack>
        )}
      </Stack>
    </Alert>
  );
}

/**
 * The inline flag on a recipe row or a planned-meal row: short, one line, and
 * it says "couldn't check" rather than nothing when that is the truth.
 */
export function AllergyChip({ report }: { report: AllergyReport }) {
  if (report.state === "none-saved" || report.state === "checked-clear") return null;
  const isWarn = report.warnings.length > 0;
  const label = isWarn
    ? report.warnings.length === 1
      ? `${report.warnings[0].person || "Allergy"} · ${report.warnings[0].allergy || "allergen"}`
      : `${report.warnings.length} allergy warnings`
    : report.state === "not-checked"
      ? "Not allergy-checked"
      : "Couldn't check allergies";
  const color = isWarn ? "var(--warn-ink)" : "var(--danger)";
  return (
    <span
      data-allergy-chip={report.state}
      style={{
        display: "inline-flex",
        alignItems: "center",
        gap: 4,
        padding: "1px 8px",
        borderRadius: 9,
        fontSize: 12,
        fontWeight: 600,
        color,
        background: "var(--warn-bg)",
        border: `1px solid ${isWarn ? "var(--warn-line)" : "var(--danger)"}`,
      }}
    >
      <IconAlertTriangle size={12} stroke={ICON_STROKE} aria-hidden />
      {label}
    </span>
  );
}
