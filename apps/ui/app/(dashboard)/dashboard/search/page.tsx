"use client";

import { useState, useMemo } from "react";
import { useSearchParams, useRouter } from "next/navigation";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { apiFetch, ApiError } from "@/lib/api";
import { searchKeys, propertyKeys } from "@/lib/query-keys";
import Select from "@/components/ui/Select";
import {
  TextInput,
  Card,
  Badge,
  Text,
  Loader,
  Title,
  Group,
  Stack,
  Button,
  Chip,
  Box,
  rem,
} from "@mantine/core";
import { IconSearch } from "@tabler/icons-react";
import type { SearchResult, SearchResponse, Property } from "@/lib/types/api";

interface EntityTypeConfig {
  label: string;
  icon: string;
  /** Route resolver — return null if no detail page exists */
  route: (id: string) => string | null;
}

const ENTITY_TYPES: Record<string, EntityTypeConfig> = {
  property: {
    label: "Properties",
    icon: "🏠",
    route: (id) => `/dashboard/properties/${id}`,
  },
  asset: {
    label: "Assets",
    icon: "📦",
    route: (id) => `/dashboard/assets/${id}`,
  },
  maintenance: {
    label: "Maintenance",
    icon: "🔧",
    route: () => `/dashboard/maintenance`,
  },
  vehicle: {
    label: "Vehicles",
    icon: "🚗",
    route: () => `/dashboard/vehicles`,
  },
  pet: {
    label: "Pets",
    icon: "🐾",
    route: () => `/dashboard/pets`,
  },
  vendor: {
    label: "Vendors",
    icon: "🏢",
    route: () => `/dashboard/vendors`,
  },
  bill: {
    label: "Bills",
    icon: "💰",
    route: () => `/dashboard/bills`,
  },
  note: {
    label: "Notes",
    icon: "📝",
    route: () => null, // notes don't have their own detail page
  },
  file: {
    label: "Files",
    icon: "📄",
    route: () => null, // files don't have their own detail page
  },
  calendar: {
    label: "Calendar",
    icon: "📅",
    route: () => `/dashboard/calendar`,
  },
  secret: {
    label: "Secrets",
    icon: "🔐",
    route: () => null, // secrets are viewed from entity detail pages
  },
};

const FILTER_TYPES = [
  { value: "", label: "All" },
  { value: "property", label: "Properties" },
  { value: "asset", label: "Assets" },
  { value: "maintenance", label: "Maintenance" },
  { value: "vehicle", label: "Vehicles" },
  { value: "pet", label: "Pets" },
  { value: "vendor", label: "Vendors" },
  { value: "bill", label: "Bills" },
  { value: "note", label: "Notes" },
  { value: "file", label: "Files" },
  { value: "calendar", label: "Calendar" },
  { value: "secret", label: "Secrets" },
];

/** Truncate body text to a reasonable snippet length */
function snippet(text: string, maxLen = 120): string {
  if (text.length <= maxLen) return text;
  return text.slice(0, maxLen).replace(/\s+\S*$/, "") + "…";
}

export default function SearchPage() {
  const searchParams = useSearchParams();
  const router = useRouter();

  const q = searchParams.get("q") ?? "";
  const [typeFilter, setTypeFilter] = useState("");
  const [propertyFilter, setPropertyFilter] = useState("");

  // Fetch properties for the filter dropdown
  const { data: properties = [] } = useQuery({
    queryKey: propertyKeys.all,
    queryFn: () =>
      apiFetch<{ data: Property[] }>("/api/v1/properties").then((r) => r.data),
    staleTime: 30_000,
  });

  // Fetch search results
  const {
    data: results = [],
    isLoading,
    isError,
    error,
  } = useQuery({
    queryKey: searchKeys.results(q, typeFilter, propertyFilter),
    queryFn: () =>
      apiFetch<{ data: SearchResponse }>("/api/v1/search", {
        params: {
          q,
          type: typeFilter || undefined,
          property_id: propertyFilter || undefined,
        },
      }).then((r) => r.data.results),
    enabled: q.length > 0,
  });

  const errorMessage =
    error instanceof ApiError ? error.message : "Search failed";

  // Group results by entity_type, preserving insertion order
  const grouped = useMemo(() => {
    const map = new Map<string, SearchResult[]>();
    for (const r of results) {
      const group = map.get(r.entity_type);
      if (group) {
        group.push(r);
      } else {
        map.set(r.entity_type, [r]);
      }
    }
    return map;
  }, [results]);

  // --- Empty query state (no ?q= in URL) ---
  if (!q) {
    return (
      <Box p="md">
        <Title order={1} size="h2">
          Search
        </Title>
        <Text size="sm" c="dimmed" mt="xs">
          Find anything across your Home OS — properties, assets, maintenance
          tasks, vehicles, and more.
        </Text>
        <Box mt="xl" display="flex" style={{ flexDirection: "column", alignItems: "center", justifyContent: "center", textAlign: "center" }}>
          <IconSearch style={{ width: rem(64), height: rem(64), color: "var(--mantine-color-gray-3)" }} />
          <Title order={3} mt="md">
            Start typing to search
          </Title>
          <Text size="sm" c="dimmed" mt="xs">
            Use the search box in the sidebar or press{" "}
            <Box
              component="kbd"
              style={{
                borderRadius: "var(--mantine-radius-sm)",
                border: "1px solid var(--mantine-color-gray-3)",
                backgroundColor: "var(--mantine-color-gray-1)",
                padding: "0.25rem 0.375rem",
                fontSize: "var(--mantine-font-size-xs)",
                fontFamily: "var(--mantine-font-family-monospace)",
              }}
            >
              Cmd+K
            </Box>
          </Text>
        </Box>
      </Box>
    );
  }

  // --- Loading state ---
  if (isLoading) {
    return (
      <Box p="md">
        <Box mb="md">
          <Title order={1} size="h2">
            Search
          </Title>
        </Box>
        <Stack>
          {[1, 2, 3, 4].map((i) => (
            <Card key={i} withBorder>
              <Box h={16} style={{ backgroundColor: "var(--mantine-color-gray-2)", borderRadius: "var(--mantine-radius-md)", marginBottom: "var(--mantine-spacing-sm)" }} />
              <Box h={12} style={{ backgroundColor: "var(--mantine-color-gray-1)", borderRadius: "var(--mantine-radius-md)" }} />
            </Card>
          ))}
        </Stack>
      </Box>
    );
  }

  // --- Error state ---
  if (isError) {
    return (
      <Box p="md">
        <Title order={1} size="h2">
          Search
        </Title>
        <Box mt="xl" display="flex" style={{ flexDirection: "column", alignItems: "center", justifyContent: "center" }}>
          <Card withBorder style={{ textAlign: "center" }}>
            <Text fw={500} c="red">
              Search failed
            </Text>
            <Text size="sm" c="dimmed" mt="xs">
              {errorMessage}
            </Text>
            <Button mt="md" onClick={() => window.location.reload()}>
              Retry
            </Button>
          </Card>
        </Box>
      </Box>
    );
  }

  // --- Results ---
  return (
    <Box p="md">
      {/* Header */}
      <Box mb="md">
        <Title order={1} size="h2">
          Search
        </Title>
        <Text size="sm" c="dimmed" mt="xs">
          {results.length > 0
            ? `${results.length} result${results.length === 1 ? "" : "s"} for "${q}"`
            : `No results for "${q}"`}
        </Text>
      </Box>

      {/* Filters row */}
      <Box mb="md">
        <Group wrap="wrap" gap="xs">
          {/* Type filter chips */}
          {FILTER_TYPES.map((ft) => (
            <Chip
              key={ft.value}
              checked={typeFilter === ft.value}
              onChange={() => setTypeFilter(ft.value)}
              variant="filled"
            >
              {ft.label}
            </Chip>
          ))}

          {/* Property filter dropdown */}
          {properties.length > 0 && (
            <Box ml="auto" style={{ flex: 1, minWidth: 200 }}>
              <Select
                label=""
                value={propertyFilter}
                onChange={(value) => setPropertyFilter(value ?? "")}
                data={properties.map((p) => ({
                  value: p.id,
                  label: p.name,
                }))}
                placeholder="All Properties"
              />
            </Box>
          )}
        </Group>
      </Box>

      {/* Empty results state */}
      {results.length === 0 ? (
        <div
          style={{
            display: "flex",
            flexDirection: "column",
            alignItems: "center",
            justifyContent: "center",
            borderRadius: "var(--mantine-radius-md)",
            border: "2px dashed var(--mantine-color-gray-3)",
            backgroundColor: "var(--mantine-color-white)",
            padding: "3rem",
            textAlign: "center",
          }}
        >
          <IconSearch style={{ width: rem(48), height: rem(48), color: "var(--mantine-color-gray-4)" }} />
          <Title order={3} mt="md">
            No results found
          </Title>
          <Text size="sm" c="dimmed" mt="xs">
            Try a different search term or clear your filters.
          </Text>
        </div>
      ) : (
        /* Results grouped by entity type */
        <Stack>
          {Array.from(grouped.entries()).map(([entityType, items]) => {
            const config = ENTITY_TYPES[entityType];
            return (
              <section key={entityType}>
                <Group mb="sm" gap="xs">
                  <Text fw={500} fz="sm" tt="uppercase" c="dimmed">
                    {config?.icon ?? "📄"} {config?.label ?? entityType}
                  </Text>
                  <Badge variant="light" color="gray" size="sm">
                    {items.length}
                  </Badge>
                </Group>
                <Stack gap="xs">
                  {items.map((result) => {
                    const href = config?.route(result.entity_id);
                    const content = (
                      <Card withBorder key={result.entity_id}>
                        <Text fw={600} size="sm">
                          {result.title}
                        </Text>
                        {result.body && (
                          <Text size="sm" c="dimmed" mt="xs">
                            {snippet(result.body)}
                          </Text>
                        )}
                        {config && (
                          <Badge mt="xs" variant="light" color="gray" size="sm">
                            {config.label}
                          </Badge>
                        )}
                      </Card>
                    );

                    if (href) {
                      return (
                        <Link key={result.entity_id} href={href}>
                          {content}
                        </Link>
                      );
                    }
                    return <div key={result.entity_id}>{content}</div>;
                  })}
                </Stack>
              </section>
            );
          })}
        </Stack>
      )}
    </Box>
  );
}
