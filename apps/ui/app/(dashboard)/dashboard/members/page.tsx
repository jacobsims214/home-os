"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import ConfirmDialog from "@/components/ui/ConfirmDialog";
import { Modal, Button, TextInput, Select, Stack, Group } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconPlus } from "@tabler/icons-react";

// ─── Types ────────────────────────────────────────────────────

interface Member {
  id: string;
  user_id: string;
  name: string;
  email: string;
  role: string;
  created_at: string;
}

interface MembersResponse {
  data: Member[];
}

const ROLE_LABELS: Record<string, string> = {
  owner: "Owner",
  family_manager: "Family Manager",
  family_member: "Family Member",
  viewer: "Viewer",
  housesitter: "House Sitter",
  vendor: "Vendor",
};

const AVAILABLE_ROLES = [
  { value: "family_manager", label: "Family Manager" },
  { value: "family_member", label: "Family Member" },
  { value: "viewer", label: "Viewer" },
  { value: "housesitter", label: "House Sitter" },
  { value: "vendor", label: "Vendor" },
];

// ─── Page ─────────────────────────────────────────────────────

export default function MembersPage() {
  const queryClient = useQueryClient();
  const [changeRoleFor, setChangeRoleFor] = useState<Member | null>(null);
  const [selectedRole, setSelectedRole] = useState("");
  const [removeMember, setRemoveMember] = useState<Member | null>(null);
  const [inviteModalOpen, setInviteModalOpen] = useState(false);
  const [inviteEmail, setInviteEmail] = useState("");
  const [inviteRole, setInviteRole] = useState<string | null>(null);

  const inviteMutation = useMutation({
    mutationFn: (body: { email: string; role: string }) =>
      apiFetch("/api/v1/invites", { method: "POST", body }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["household", "members"] });
      notifications.show({ message: "Invite sent!", color: "green" });
      setInviteModalOpen(false);
      setInviteEmail("");
      setInviteRole(null);
    },
    onError: (err: Error) => {
      notifications.show({ message: err.message, color: "red" });
    },
  });

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ["household", "members"],
    queryFn: () => apiFetch<MembersResponse>("/api/v1/households/me/members"),
    staleTime: 30_000,
  });

  const members = data?.data ?? [];

  const changeRoleMutation = useMutation({
    mutationFn: ({ userId, role }: { userId: string; role: string }) =>
      apiFetch(`/api/v1/households/me/members/${userId}`, {
        method: "PATCH",
        body: { role },
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["household", "members"] });
      setChangeRoleFor(null);
      setSelectedRole("");
    },
  });

  const removeMutation = useMutation({
    mutationFn: (userId: string) =>
      apiFetch(`/api/v1/households/me/members/${userId}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["household", "members"] });
      setRemoveMember(null);
    },
  });

  const handleChangeRole = () => {
    if (!changeRoleFor || !selectedRole) return;
    changeRoleMutation.mutate({ userId: changeRoleFor.user_id, role: selectedRole });
  };

  return (
    <div className="px-4 py-6 sm:px-6 lg:px-8">
      <div className="sm:flex sm:items-center sm:justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">Family Members</h1>
          <p className="mt-1 text-sm text-gray-500">
            Manage who has access to your household.
          </p>
        </div>
        <Button leftSection={<IconPlus size={16} />} onClick={() => setInviteModalOpen(true)}>
          Invite Member
        </Button>
      </div>

      {/* Loading */}
      {isLoading && (
        <div className="mt-6 animate-pulse space-y-3">
          {[1, 2, 3].map((i) => (
            <div key={i} className="h-16 rounded-lg bg-gray-200" />
          ))}
        </div>
      )}

      {/* Error */}
      {isError && (
        <div className="mt-6 rounded-lg border border-red-200 bg-red-50 p-4">
          <p className="text-sm text-red-700">
            {error instanceof Error ? error.message : "Failed to load members"}
          </p>
        </div>
      )}

      {/* Members table */}
      {!isLoading && !isError && (
        <div className="mt-6 overflow-hidden rounded-lg border border-gray-200">
          <table className="min-w-full divide-y divide-gray-300">
            <thead className="bg-gray-50">
              <tr>
                <th className="px-4 py-3 text-left text-sm font-semibold text-gray-900">Name</th>
                <th className="px-4 py-3 text-left text-sm font-semibold text-gray-900">Email</th>
                <th className="px-4 py-3 text-left text-sm font-semibold text-gray-900">Role</th>
                <th className="px-4 py-3 text-left text-sm font-semibold text-gray-900">Joined</th>
                <th className="px-4 py-3 text-right text-sm font-semibold text-gray-900">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-200 bg-white">
              {members.length === 0 ? (
                <tr>
                  <td colSpan={5} className="px-4 py-8 text-center text-sm text-gray-400">
                    No members found.
                  </td>
                </tr>
              ) : (
                members.map((member) => (
                  <tr key={member.id} className="hover:bg-gray-50">
                    <td className="whitespace-nowrap px-4 py-3 text-sm font-medium text-gray-900">
                      <div className="flex items-center gap-2">
                        <span className="inline-flex h-7 w-7 items-center justify-center rounded-full bg-indigo-50 text-xs font-medium text-indigo-700">
                          {member.name.charAt(0).toUpperCase()}
                        </span>
                        {member.name}
                      </div>
                    </td>
                    <td className="whitespace-nowrap px-4 py-3 text-sm text-gray-600">
                      {member.email}
                    </td>
                    <td className="whitespace-nowrap px-4 py-3">
                      <span className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ${
                        member.role === "owner"
                          ? "bg-purple-50 text-purple-700"
                          : member.role === "family_manager"
                          ? "bg-blue-50 text-blue-700"
                          : "bg-gray-50 text-gray-600"
                      }`}>
                        {ROLE_LABELS[member.role] ?? member.role}
                      </span>
                    </td>
                    <td className="whitespace-nowrap px-4 py-3 text-sm text-gray-500">
                      {member.created_at
                        ? new Date(member.created_at).toLocaleDateString("en-US", {
                            month: "short",
                            year: "numeric",
                          })
                        : "—"}
                    </td>
                    <td className="whitespace-nowrap px-4 py-3 text-right text-sm">
                      {member.role !== "owner" ? (
                        <div className="flex justify-end gap-1">
                          <button
                            onClick={() => { setChangeRoleFor(member); setSelectedRole(member.role); }}
                            className="inline-flex items-center rounded-md px-2.5 py-1.5 text-xs font-medium text-indigo-600 hover:bg-indigo-50 transition-colors"
                          >
                            Change Role
                          </button>
                          <button
                            onClick={() => setRemoveMember(member)}
                            className="inline-flex items-center rounded-md px-2.5 py-1.5 text-xs font-medium text-red-600 hover:bg-red-50 transition-colors"
                          >
                            Remove
                          </button>
                        </div>
                      ) : (
                        <span className="text-xs text-gray-400">—</span>
                      )}
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      )}

      {/* Change Role Modal */}
      {changeRoleFor && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
          <div className="w-full max-w-sm rounded-lg bg-white p-6 shadow-xl">
            <h3 className="text-lg font-semibold text-gray-900">
              Change Role — {changeRoleFor.name}
            </h3>
            <p className="mt-1 text-sm text-gray-500">Current role: {ROLE_LABELS[changeRoleFor.role] ?? changeRoleFor.role}</p>
            <div className="mt-4 space-y-4">
              <select
                value={selectedRole}
                onChange={(e) => setSelectedRole(e.target.value)}
                className="block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
              >
                <option value="">Select role...</option>
                {AVAILABLE_ROLES.map((r) => (
                  <option key={r.value} value={r.value}>{r.label}</option>
                ))}
              </select>
              <div className="flex justify-end gap-2">
                <button
                  onClick={() => { setChangeRoleFor(null); setSelectedRole(""); }}
                  className="inline-flex items-center rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50"
                >
                  Cancel
                </button>
                <button
                  onClick={handleChangeRole}
                  disabled={!selectedRole || changeRoleMutation.isPending}
                  className="inline-flex items-center rounded-md bg-indigo-600 px-3 py-1.5 text-sm font-semibold text-white hover:bg-indigo-500 disabled:bg-indigo-400"
                >
                  {changeRoleMutation.isPending ? "Saving..." : "Save"}
                </button>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* Invite Member Modal */}
      <Modal
        opened={inviteModalOpen}
        onClose={() => { setInviteModalOpen(false); setInviteEmail(""); setInviteRole(null); }}
        title="Invite Member"
        size="sm"
        centered
      >
        <Stack>
          <TextInput
            label="Email"
            value={inviteEmail}
            onChange={(e) => setInviteEmail(e.target.value)}
            placeholder="email@example.com"
            required
          />
          <Select
            label="Role"
            value={inviteRole}
            onChange={setInviteRole}
            placeholder="Select a role"
            data={[
              { value: "family_manager", label: "Family Manager" },
              { value: "family_member", label: "Family Member" },
              { value: "viewer", label: "Viewer" },
            ]}
            required
          />
          <Group justify="flex-end" mt="md">
            <Button
              variant="default"
              onClick={() => { setInviteModalOpen(false); setInviteEmail(""); setInviteRole(null); }}
            >
              Cancel
            </Button>
            <Button
              onClick={() => {
                if (!inviteEmail.trim() || !inviteRole) return;
                inviteMutation.mutate({ email: inviteEmail.trim(), role: inviteRole });
              }}
              loading={inviteMutation.isPending}
            >
              Send Invite
            </Button>
          </Group>
        </Stack>
      </Modal>

      {/* Remove confirmation */}
      <ConfirmDialog
        open={removeMember !== null}
        onClose={() => setRemoveMember(null)}
        onConfirm={() => removeMember && removeMutation.mutate(removeMember.user_id)}
        title="Remove Member"
        message={
          removeMember
            ? `Are you sure you want to remove ${removeMember.name} from the household? They will lose all access.`
            : ""
        }
        loading={removeMutation.isPending}
      />
    </div>
  );
}