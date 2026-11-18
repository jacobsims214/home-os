"use client";

/**
 * EntityDetail — Config-driven component for displaying/editing any entity type.
 *
 * Accepts a fields array (defining form inputs) and sections array (grouping fields
 * into Card sections). Fetches entity data, displays in read-only mode, and toggles
 * to edit mode with a Mantine Form. Supports save (PUT) and delete (with confirmation).
 */

import { useState, useEffect, type ReactNode } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { apiFetch } from "@/lib/api";
import { Card, Text, Group, Stack, Button, Modal, Alert, Skeleton, Box, Textarea } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { TextInput, Select } from "@/components/ui";
import DetailPageLayout from "@/components/layout/DetailPageLayout";
import { BOOLEAN_OPTIONS, booleanToOption, optionToBoolean } from "./boolean-field";

// ─── Types ────────────────────────────────────────────────────

export interface FieldConfig {
  name: string;
  label: string;
  type: "text" | "textarea" | "select" | "number" | "date" | "boolean" | "custom";
  options?: { value: string; label: string }[];
  required?: boolean;
  /**
   * Shown, never edited or sent: it stays out of the form values and the PUT
   * body, and edit mode shows renderView (e.g. a vehicle's linked loans and
   * bills, #4706).
   */
  readOnly?: boolean;
  /** type "custom": the read-only value (default: the raw value as text). */
  renderView?: (entity: Record<string, unknown>) => ReactNode;
  /** type "custom": the edit control for this field's value (for example the pet's VetPicker, #4703). */
  renderEdit?: (args: {
    value: unknown;
    onChange: (value: unknown) => void;
    entity: Record<string, unknown>;
  }) => ReactNode;
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
        if (field.readOnly) return;
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
    const custom = getFieldConfig(fieldName)?.renderView;
    if (custom) return custom(entity);
    const value = getFieldValue(entity, fieldName);
    return formatDisplayValue(value);
  };

  // Render field input in edit mode
  const renderFieldInput = (field: FieldConfig) => {
    if (field.readOnly) {
      return <div key={field.name}>{renderFieldValue(field.name)}</div>;
    }
    if (field.type === "custom" && field.renderEdit) {
      return (
        <div key={field.name}>
          {field.renderEdit({
            value: formValues[field.name] ?? null,
            onChange: (v) => handleFormChange(field.name, v),
            entity,
          })}
        </div>
      );
    }
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
          // #4728: the record holds a boolean, the Select strings; map both
          // ways so the saved value shows and a choice is sent as a boolean.
          // No deselect: an untouched unset value stays unset (not sent).
          <Select
            key={field.name}
            {...baseProps}
            value={booleanToOption(formValues[field.name])}
            placeholder="Not set"
            allowDeselect={false}
            onChange={(value: string | null) => {
              const next = optionToBoolean(value);
              if (next !== undefined) handleFormChange(field.name, next);
            }}
            data={BOOLEAN_OPTIONS}
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
        // Below md, inputs grow to a 44 px touch target with 16 px text (no iOS zoom).
        <Stack className="max-md:[&_.mantine-Input-input]:min-h-11 max-md:[&_.mantine-Input-input]:text-base">
          {(sections ?? []).map((section) => (
            <Box key={section.title} mb="md">
              <Text size="sm" fw={600} mb="sm" className="text-ink-2">
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
            <div key={section.title} className="flex flex-col gap-2 mb-6">
              <Text size="sm" fw={600} className="text-ink-2">
                {section.title}
              </Text>
              {/* Phone: label above value. md+: label/value grid. */}
              <dl className="m-0 divide-y divide-line">
                {section.fields.map((fieldName) => {
                  const field = getFieldConfig(fieldName);
                  if (!field) return null;
                  // A read-only block (readOnly) is full width; the section title labels it.
                  return (
                    <div
                      key={fieldName}
                      data-field={fieldName}
                      className={
                        field.readOnly
                          ? "py-2.5"
                          : "flex flex-col gap-1 py-2.5 md:grid md:grid-cols-[minmax(7rem,1fr)_minmax(0,2fr)] md:gap-x-6"
                      }
                    >
                      <dt className={field.readOnly ? "sr-only" : "text-sm font-medium text-ink-3"}>{field.label}</dt>
                      <dd className="m-0 text-sm text-ink break-words [overflow-wrap:anywhere] whitespace-pre-line">
                        {renderFieldValue(fieldName)}
                      </dd>
                    </div>
                  );
                })}
              </dl>
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
