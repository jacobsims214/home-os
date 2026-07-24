"use client";

import Link from "next/link";
import { Card, Text, Group, Button } from "@mantine/core";
import EntityResources from "@/components/EntityResources";

interface DetailPageLayoutProps {
  entityType: string;
  entityId: string;
  title: string;
  isEditing?: boolean;
  onEdit?: () => void;
  onDelete?: () => void;
  onCancel?: () => void;
  onSave?: () => void;
  isSaving?: boolean;
  children: React.ReactNode;
}

export default function DetailPageLayout({
  entityType,
  entityId,
  title,
  isEditing,
  onEdit,
  onDelete,
  onCancel,
  onSave,
  isSaving,
  children,
}: DetailPageLayoutProps) {
  return (
    <div className="mx-auto max-w-5xl px-4 py-6 sm:px-6 lg:px-8">
      {/* Header bar */}
      <div className="mb-6 flex items-center justify-between">
        <div>
          <Link
            href={`/dashboard/${entityType}s`}
            className="text-sm text-gray-500 hover:text-gray-700"
          >
            ← Back to {entityType.charAt(0).toUpperCase() + entityType.slice(1)}s
          </Link>
          <Text size="xl" fw={700} mt={2}>
            {title}
          </Text>
        </div>
        <Group gap="sm">
          {isEditing ? (
            <>
              <Button variant="default" onClick={onCancel}>Cancel</Button>
              <Button onClick={onSave} loading={isSaving}>Save</Button>
            </>
          ) : (
            <>
              <Button variant="default" onClick={onEdit}>Edit</Button>
              {onDelete && (
                <Button color="red" variant="outline" onClick={onDelete}>Delete</Button>
              )}
            </>
          )}
        </Group>
      </div>

      {/* Sheet - white card container */}
      <Card shadow="sm" radius="md" withBorder p="lg" mb="lg">
        {children}
      </Card>

      {/* EntityResources */}
      <EntityResources entityType={entityType} entityId={entityId} />
    </div>
  );
}
