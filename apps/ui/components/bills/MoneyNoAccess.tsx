"use client";

/**
 * The card a role without money read gets when it reaches a bills page
 * directly (#4551, mockup artifact #102's housesitter state).
 *
 * A housesitter and a vendor have no Money entry in the nav at all (the nav
 * config's section list decides that, #4543), so the only way here is an old
 * link or a bookmark. The API answers 403 on every money route for them
 * (decisions/2026-11-15-role-policy.md), so the page must not ask: it says
 * what is true and offers the way back, rather than rendering an empty list
 * that reads as "no bills".
 */

import Link from "next/link";
import { Button, Card, Stack, Text, ThemeIcon, Title } from "@mantine/core";
import { IconLock } from "@tabler/icons-react";

export default function MoneyNoAccess({ role }: { role: string | null | undefined }) {
  const who = role === "housesitter" ? "housesitters" : role === "vendor" ? "vendors" : "everyone";
  return (
    <Card withBorder radius="md" padding="lg" className="mx-auto max-w-lg text-center" data-testid="money-no-access">
      <Stack align="center" gap="sm">
        <ThemeIcon size={48} radius="lg" variant="light" aria-hidden>
          <IconLock size={24} stroke={1.75} />
        </ThemeIcon>
        <Title order={3} className="text-lg font-semibold">
          Money isn&apos;t shared with {who}
        </Title>
        <Text size="sm" c="dimmed">
          Bills and what the family owes stay with the family. If something needs paying while you&apos;re here, ask
          them.
        </Text>
        <Button component={Link} href="/dashboard" className="min-h-11">
          Go to Today
        </Button>
      </Stack>
    </Card>
  );
}
