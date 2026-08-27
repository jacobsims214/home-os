"use client";

import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import { TextInput, PasswordInput, Button, Stack, Group, Paper, Title, Text } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconUserPlus } from "@tabler/icons-react";

interface AdminCreateUserResponse {
  user_id: string;
  household_id: string;
}

export default function AdminPage() {
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [householdName, setHouseholdName] = useState("");

  const createUserMutation = useMutation({
    mutationFn: (body: {
      email: string;
      name: string;
      password: string;
      household_name?: string;
    }) =>
      apiFetch<AdminCreateUserResponse>("/api/v1/admin/users", {
        method: "POST",
        body,
      }),
    onSuccess: (data) => {
      notifications.show({
        title: "User created",
        message: `User ID: ${data.user_id}, Household ID: ${data.household_id}`,
        color: "green",
      });
      setEmail("");
      setName("");
      setPassword("");
      setHouseholdName("");
    },
    onError: (err: Error) => {
      notifications.show({
        title: "Error",
        message: err.message,
        color: "red",
      });
    },
  });

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!email.trim() || !name.trim() || !password) return;
    createUserMutation.mutate({
      email: email.trim(),
      name: name.trim(),
      password,
      household_name: householdName.trim() || undefined,
    });
  };

  return (
    <div className="px-4 py-6 sm:px-6 lg:px-8">
      <div className="sm:flex sm:items-center sm:justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">Admin</h1>
          <p className="mt-1 text-sm text-gray-500">
            Create new users and households.
          </p>
        </div>
      </div>

      <Paper withBorder p="lg" radius="md" className="mt-6 max-w-lg">
        <form onSubmit={handleSubmit}>
          <Stack>
            <TextInput
              label="Email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="email@example.com"
              required
            />
            <TextInput
              label="Name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Full name"
              required
            />
            <PasswordInput
              label="Password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="Min 8 characters"
              required
              minLength={8}
            />
            <TextInput
              label="Household Name"
              value={householdName}
              onChange={(e) => setHouseholdName(e.target.value)}
              placeholder='Defaults to "{name}&apos;s Home"'
            />
            <Group justify="flex-end" mt="md">
              <Button
                type="submit"
                leftSection={<IconUserPlus size={16} />}
                loading={createUserMutation.isPending}
                disabled={!email.trim() || !name.trim() || !password}
              >
                Create User
              </Button>
            </Group>
          </Stack>
        </form>
      </Paper>

      {createUserMutation.isSuccess && (
        <Paper withBorder p="lg" radius="md" className="mt-4 max-w-lg bg-green-50">
          <Text size="sm" c="green">
            User created successfully.
          </Text>
        </Paper>
      )}
    </div>
  );
}