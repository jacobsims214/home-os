"use client";

/**
 * EntityDetail — Config-driven component for displaying/editing any entity type.
 *
 * Accepts a fields array (defining form inputs) and sections array (grouping fields
 * into Card sections). Fetches entity data, displays in read-only mode, and toggles
 * to edit mode with a Mantine Form. Supports save (PUT) and delete (with confirmation).
 */

import { useState, useEffect } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { apiFetch } from "@/lib/api";
import { Card, Text, Group, Stack, Button, Modal, Alert, Skeleton, Box, Textarea } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { TextInput, Select } from "@/components/ui";
import DetailPageLayout from "@/components/layout/DetailPageLayout";

// ─── Types ────────────────────────────────────────────────────

export interface FieldConfig {
  name: string;
  label: string;
  type: "text" | "textarea" | "select" | "number" | "date" | "boolean";
  options?: { value: string; label: string }[];
  required?: boolean;
}

export interface SectionConfig {
  title: string;
  fields: string[]; // field names that exist in fields array
}

export interface EntityDetailProps {
  entityType: string;
  entityId: string;
  fields?: FieldConfig[];
  sections?: SectionConfig[];
}

// ─── Default field configurations per entity type ─────────────

const DEFAULT_FIELDS: Record<string, FieldConfig[]> = {
  vendor: [
    { name: "name", label: "Name", type: "text", required: true },
    { name: "specialty", label: "Specialty", type: "select", options: [
      { value: "HVAC", label: "HVAC" },
      { value: "Plumbing", label: "Plumbing" },
      { value: "Electrical", label: "Electrical" },
      { value: "Roofing", label: "Roofing" },
      { value: "Landscaping", label: "Landscaping" },
      { value: "Cleaning", label: "Cleaning" },
      { value: "Pest Control", label: "Pest Control" },
      { value: "General Contractor", label: "General Contractor" },
      { value: "Other", label: "Other" },
    ] },
    { name: "phone", label: "Phone", type: "text" },
    { name: "email", label: "Email", type: "text" },
    { name: "website", label: "Website", type: "text" },
    { name: "property_id", label: "Property", type: "select", options: [] },
    { name: "notes", label: "Notes", type: "textarea" },
  ],
  bill: [
    { name: "name", label: "Name", type: "text", required: true },
    { name: "amount", label: "Amount", type: "number", required: true },
    { name: "date", label: "Date", type: "date", required: true },
    { name: "property_id", label: "Property", type: "select", options: [] },
    { name: "notes", label: "Notes", type: "textarea" },
  ],
  property: [
    { name: "name", label: "Name", type: "text", required: true },
    { name: "address", label: "Address", type: "text" },
    { name: "city", label: "City", type: "text" },
    { name: "state", label: "State", type: "text" },
    { name: "zip", label: "ZIP Code", type: "text" },
    { name: "bedrooms", label: "Bedrooms", type: "number" },
    { name: "bathrooms", label: "Bathrooms", type: "number" },
    { name: "sqft", label: "Square Feet", type: "number" },
    { name: "year_built", label: "Year Built", type: "number" },
    { name: "notes", label: "Notes", type: "textarea" },
  ],
};

const DEFAULT_SECTIONS: Record<string, SectionConfig[]> = {
  vendor: [
    { title: "Basic Information", fields: ["name", "specialty"] },
    { title: "Contact", fields: ["phone", "email", "website"] },
    { title: "Details", fields: ["property_id", "notes"] },
  ],
  bill: [
    { title: "Bill Information", fields: ["name", "amount", "date"] },
    { title: "Details", fields: ["property_id", "notes"] },
  ],
  property: [
    { title: "Basic Information", fields: ["name", "address"] },
    { title: "Location", fields: ["city", "state", "zip"] },
    { title: "Details", fields: ["bedrooms", "bathrooms", "sqft", "year_built", "notes"] },
  ],
};

// ─── Helper: Get field value by name ─────────────────────────

function getFieldValue(entity: Record<string, unknown>, fieldName: string): unknown {
  return entity[fieldName];
}

// ─── Helper: Format value for display ────────────────────────

function formatDisplayValue(value: unknown): string {
  if (value == null) return "—";
  if (typeof value === "boolean") return value ? "Yes" : "No";
  if (typeof value === "number") return value.toString();
  return String(value);
}

// ─── Component ────────────────────────────────────────────────

export default function EntityDetail({
  entityType,
  entityId,
  fields,
  sections,
}: EntityDetailProps) {
  const router = useRouter();
  const queryClient = useQueryClient();
  const [isEditing, setIsEditing] = useState(false);
  const [deleteModalOpen, setDeleteModalOpen] = useState(false);

  // Fetch entity data
  const { data, isLoading, isError, error } = useQuery({
    queryKey: [entityType, entityId],
    queryFn: () =>
      apiFetch<{ data: Record<string, unknown> }>(
        `/api/v1/${entityType}s/${entityId}`
      ),
  });

  const entity = data?.data ?? null;

  // Save mutation
  const saveMutation = useMutation({
    mutationFn: (updatedEntity: Record<string, unknown>) =>
      apiFetch<{ data: Record<string, unknown> }>(
        `/api/v1/${entityType}s/${entityId}`,
        {
          method: "PUT",
          body: updatedEntity,
        }
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: [entityType, entityId] });
      setIsEditing(false);
    },
    onError: (error: Error) => {
      notifications.show({
        title: "Save failed",
        message: error.message,
        color: "red",
      });
    },
  });

  // Delete mutation
  const deleteMutation = useMutation({
    mutationFn: () =>
      apiFetch<void>(`/api/v1/${entityType}s/${entityId}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: [entityType] });
      router.push(`/${entityType}s`);
    },
    onError: (error: Error) => {
      notifications.show({
        title: "Delete failed",
        message: error.message,
        color: "red",
      });
    },
  });

  // Form state
  const [formValues, setFormValues] = useState<Record<string, unknown>>({});

  // Initialize form values when entity loads
  useEffect(() => {
    if (entity && Object.keys(entity).length > 0 && Object.keys(formValues).length === 0 && !isEditing) {
      const initialValues: Record<string, unknown> = {};
      (fields ?? []).forEach((field) => {
        initialValues[field.name] = getFieldValue(entity, field.name);
      });
      setFormValues(initialValues);
    }
  }, [entity, fields, isEditing]);

  // Handle form field change
  const handleFormChange = (name: string, value: unknown) => {
    setFormValues((prev) => ({ ...prev, [name]: value }));
  };

  // Handle save
  const handleSave = () => {
    saveMutation.mutate(formValues);
  };

  // Handle delete
  const handleDelete = () => {
    deleteMutation.mutate();
    setDeleteModalOpen(false);
  };

  // Loading state
  if (isLoading) {
    return (
      <Stack>
        <Skeleton height={200} radius="md" />
        {(sections ?? []).map((section, idx) => (
          <Card key={idx} shadow="sm" radius="md" withBorder p={5} mb={4}>
            <Skeleton height={20} width="40%" radius="sm" mb="sm" />
            <Skeleton height={100} radius="sm" />
          </Card>
        ))}
      </Stack>
    );
  }

  // Error state
  if (isError) {
    return (
      <Alert color="red" title="Error loading entity">
        {error instanceof Error ? error.message : "Failed to load entity"}
      </Alert>
    );
  }

  // Entity not found
  if (!entity) {
    return <Text>No {entityType} found with ID {entityId}</Text>;
  }

  // Get field config by name
  const getFieldConfig = (name: string): FieldConfig | undefined =>
    (fields ?? []).find((f) => f.name === name);

  // Render field value in read-only mode
  const renderFieldValue = (fieldName: string) => {
    const value = getFieldValue(entity, fieldName);
    return <Text size="sm" c="gray.9">{formatDisplayValue(value)}</Text>;
  };

  // Render field input in edit mode
  const renderFieldInput = (field: FieldConfig) => {
    const value = formValues[field.name] ?? "";
    const baseProps = {
      label: field.label,
      value: value as string,
      required: field.required,
      placeholder: `Enter ${field.label.toLowerCase()}`,
    };

    switch (field.type) {
      case "textarea":
        return (
          <Textarea
            key={field.name}
            label={baseProps.label}
            value={baseProps.value}
            onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) =>
              handleFormChange(field.name, e.target.value)
            }
            required={baseProps.required}
            placeholder={baseProps.placeholder}
            rows={4}
          />
        );
      case "select":
        return (
          <Select
            key={field.name}
            {...baseProps}
            onChange={(value: string | null) =>
              handleFormChange(field.name, value ?? "")
            }
            data={field.options ?? []}
          />
        );
      case "number":
        return (
          <TextInput
            key={field.name}
            {...baseProps}
            onChange={(e: React.ChangeEvent<HTMLInputElement>) =>
              handleFormChange(field.name, e.target.value)
            }
            type="number"
          />
        );
      case "date":
        return (
          <TextInput
            key={field.name}
            {...baseProps}
            onChange={(e: React.ChangeEvent<HTMLInputElement>) =>
              handleFormChange(field.name, e.target.value)
            }
            type="date"
          />
        );
      case "boolean":
        return (
          <Select
            key={field.name}
            {...baseProps}
            onChange={(value: string | null) =>
              handleFormChange(field.name, value ?? "")
            }
            data={[
              { value: "true", label: "Yes" },
              { value: "false", label: "No" },
            ]}
          />
        );
      case "text":
      default:
        return (
          <TextInput
            key={field.name}
            {...baseProps}
            onChange={(e: React.ChangeEvent<HTMLInputElement>) =>
              handleFormChange(field.name, e.target.value)
            }
          />
        );
    }
  };

  return (
    <DetailPageLayout
      entityType={entityType}
      entityId={entityId}
      title={String(entity.name || entity.title || "")}
      isEditing={isEditing}
      onEdit={() => setIsEditing(true)}
      onDelete={() => setDeleteModalOpen(true)}
      onCancel={() => setIsEditing(false)}
      onSave={handleSave}
      isSaving={saveMutation.isPending}
    >
      {isEditing ? (
        // edit form content
        <Stack>
          {(sections ?? []).map((section) => (
            <Box key={section.title} mb="md">
              <Text size="sm" fw={600} mb="sm" c="gray.7">
                {section.title}
              </Text>
              <Stack>
                {section.fields.map((fieldName) => {
                  const field = getFieldConfig(fieldName);
                  if (!field) return null;
                  return renderFieldInput(field);
                })}
              </Stack>
            </Box>
          ))}
        </Stack>
      ) : (
        // view content - sections with fields
        <>
          {(sections ?? []).map((section) => (
            <div key={section.title} className="flex flex-col gap-4 mb-6">
              <Text size="sm" fw={600} c="gray.7">
                {section.title}
              </Text>
              <Stack>
                {section.fields.map((fieldName) => {
                  const field = getFieldConfig(fieldName);
                  if (!field) return null;
                  return (
                    <div key={fieldName} className="flex justify-between py-2">
                      <span className="font-medium text-sm text-gray-500">{field.label}</span>
                      {renderFieldValue(fieldName)}
                    </div>
                  );
                })}
              </Stack>
            </div>
          ))}
        </>
      )}

      {/* Delete confirmation modal */}
      <Modal
        opened={deleteModalOpen}
        onClose={() => setDeleteModalOpen(false)}
        title="Delete entity"
      >
        <Text mb="sm">
          Are you sure you want to delete this {entityType}? This action cannot
          be undone.
        </Text>
        <Group justify="flex-end">
          <Button
            variant="default"
            onClick={() => setDeleteModalOpen(false)}
          >
            Cancel
          </Button>
          <Button
            variant="danger"
            onClick={handleDelete}
            loading={deleteMutation.isPending}
          >
            Delete
          </Button>
        </Group>
      </Modal>
    </DetailPageLayout>
  );
}
