"use client";

import { Card, Text, Button, Title } from "@mantine/core";
import Link from "next/link";

export default function RegisterPage() {
  return (
    <div className="flex min-h-screen items-center justify-center bg-gray-50 px-4">
      <Card className="w-full max-w-md" padding="xl">
        <div className="text-center">
          <Title order={2}>Registration</Title>
          <Text size="sm" c="dimmed" mt="md">
            New accounts can only be created by a household administrator.
          </Text>
          <Text size="sm" c="dimmed" mt="sm">
            Please contact your household admin to set up your account.
          </Text>
          <Button component={Link} href="/login" mt="lg" variant="outline" fullWidth>
            Back to Sign in
          </Button>
        </div>
      </Card>
    </div>
  );
}