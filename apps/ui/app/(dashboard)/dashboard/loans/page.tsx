"use client";

import { useState } from "react";
import Link from "next/link";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import { loanKeys } from "@/lib/query-keys";
import { Card, Text, Group, Button, Modal, TextInput, NumberInput, Select, Textarea, SimpleGrid, Stack, Skeleton } from "@mantine/core";
import { IconPlus } from "@tabler/icons-react";

interface Loan {
  id: string;
  name: string;
  entity_type?: string | null;
  entity_id?: string | null;
  lender?: string | null;
  original_amount: number;
  remaining_balance: number;
  interest_rate?: number | null;
  term_months?: number | null;
  monthly_payment?: number | null;
  start_date?: string | null;
  notes?: string | null;
  created_at: string;
  updated_at: string;
}

export default function LoansPage() {
  const queryClient = useQueryClient();
  const [addModalOpen, setAddModalOpen] = useState(false);
  const [newName, setNewName] = useState("");
  const [newLender, setNewLender] = useState("");
  const [newOriginalAmount, setNewOriginalAmount] = useState<number | "">("");
  const [newRemainingBalance, setNewRemainingBalance] = useState<number | "">("");
  const [newInterestRate, setNewInterestRate] = useState<number | "">("");
  const [newTermMonths, setNewTermMonths] = useState<number | "">("");
  const [newMonthlyPayment, setNewMonthlyPayment] = useState<number | "">("");
  const [newStartDate, setNewStartDate] = useState("");
  const [newEntityType, setNewEntityType] = useState<string | null>(null);
  const [newEntityId, setNewEntityId] = useState("");
  const [newNotes, setNewNotes] = useState("");

  const { data, isLoading } = useQuery({
    queryKey: loanKeys.lists(),
    queryFn: () => apiFetch<{ data: Loan[] }>("/api/v1/loans"),
  });

  const loans = data?.data ?? [];

  const createMutation = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      apiFetch<{ data: Loan }>("/api/v1/loans", { method: "POST", body }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: loanKeys.all });
      setAddModalOpen(false);
      setNewName("");
      setNewLender("");
      setNewOriginalAmount("");
      setNewRemainingBalance("");
      setNewInterestRate("");
      setNewTermMonths("");
      setNewMonthlyPayment("");
      setNewStartDate("");
      setNewEntityType(null);
      setNewEntityId("");
      setNewNotes("");
    },
  });

  const handleAdd = () => {
    if (!newName.trim()) return;
    createMutation.mutate({
      name: newName.trim(),
      lender: newLender.trim() || undefined,
      original_amount: Number(newOriginalAmount) || 0,
      remaining_balance: Number(newRemainingBalance) || 0,
      interest_rate: newInterestRate !== "" ? Number(newInterestRate) : undefined,
      term_months: newTermMonths !== "" ? Number(newTermMonths) : undefined,
      monthly_payment: newMonthlyPayment !== "" ? Number(newMonthlyPayment) : undefined,
      start_date: newStartDate || undefined,
      entity_type: newEntityType || undefined,
      entity_id: newEntityId.trim() || undefined,
      notes: newNotes.trim() || undefined,
    });
  };

  function fmtCurrency(n: number): string {
    return n.toLocaleString("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 0 });
  }

  return (
    <div className="mx-auto max-w-6xl px-4 py-6 sm:px-6 lg:px-8">
      <div className="mb-6 flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-gray-900">Loans</h1>
          <p className="mt-1 text-sm text-gray-500">Track mortgages, vehicle loans, and other debts</p>
        </div>
        <Button leftSection={<IconPlus size={16} />} onClick={() => setAddModalOpen(true)}>
          Add Loan
        </Button>
      </div>

      {isLoading && (
        <div className="space-y-2">
          {[1, 2, 3].map((i) => (
            <Skeleton key={i} height={56} radius="sm" />
          ))}
        </div>
      )}

      {!isLoading && loans.length === 0 && (
        <div className="flex flex-col items-center justify-center rounded-lg border-2 border-dashed border-gray-300 py-12 text-center">
          <p className="text-sm text-gray-500">No loans yet. Add your first loan to get started.</p>
        </div>
      )}

      {!isLoading && loans.length > 0 && (
        <div className="overflow-hidden rounded-xl border border-gray-200 bg-white shadow-sm">
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-200">
                <th className="px-4 py-3 text-left text-xs font-semibold uppercase text-gray-500">Name</th>
                <th className="px-4 py-3 text-left text-xs font-semibold uppercase text-gray-500">Lender</th>
                <th className="px-4 py-3 text-right text-xs font-semibold uppercase text-gray-500">Balance</th>
                <th className="px-4 py-3 text-right text-xs font-semibold uppercase text-gray-500">Rate</th>
              </tr>
            </thead>
            <tbody>
              {loans.map((loan) => (
                <tr
                  key={loan.id}
                  onClick={() => (window.location.href = `/dashboard/loans/${loan.id}`)}
                  className="cursor-pointer border-b border-gray-50 hover:bg-gray-50"
                >
                  <td className="px-4 py-3 text-sm font-medium text-gray-900">{loan.name}</td>
                  <td className="px-4 py-3 text-sm text-gray-600">{loan.lender || "—"}</td>
                  <td className="px-4 py-3 text-right text-sm font-medium text-gray-900">{fmtCurrency(loan.remaining_balance)}</td>
                  <td className="px-4 py-3 text-right text-sm text-gray-600">
                    {loan.interest_rate != null ? `${loan.interest_rate}%` : "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Add Loan Modal */}
      <Modal opened={addModalOpen} onClose={() => setAddModalOpen(false)} title="Add Loan" size="md">
        <Stack>
          <TextInput label="Loan Name" value={newName} onChange={(e) => setNewName(e.target.value)} placeholder="e.g. First Mortgage" required />
          <TextInput label="Lender" value={newLender} onChange={(e) => setNewLender(e.target.value)} placeholder="e.g. Wells Fargo" />
          <NumberInput label="Original Amount" value={newOriginalAmount} onChange={(v) => setNewOriginalAmount(v === "" ? "" : Number(v))} placeholder="$" thousandSeparator="," />
          <NumberInput label="Remaining Balance" value={newRemainingBalance} onChange={(v) => setNewRemainingBalance(v === "" ? "" : Number(v))} placeholder="$" thousandSeparator="," />
          <NumberInput label="Interest Rate (%)" value={newInterestRate} onChange={(v) => setNewInterestRate(v === "" ? "" : Number(v))} placeholder="e.g. 6.5" decimalScale={2} />
          <SimpleGrid cols={2}>
            <NumberInput label="Term (months)" value={newTermMonths} onChange={(v) => setNewTermMonths(v === "" ? "" : Number(v))} placeholder="e.g. 360" min={0} />
            <NumberInput label="Monthly Payment" value={newMonthlyPayment} onChange={(v) => setNewMonthlyPayment(v === "" ? "" : Number(v))} placeholder="$" thousandSeparator="," />
          </SimpleGrid>
          <TextInput label="Start Date" value={newStartDate} onChange={(e) => setNewStartDate(e.target.value)} type="date" />
          <Select
            label="Entity Type"
            value={newEntityType}
            onChange={setNewEntityType}
            placeholder="Select entity type"
            data={[
              { value: "property", label: "Property" },
              { value: "vehicle", label: "Vehicle" },
              { value: "asset", label: "Asset" },
              { value: "personal", label: "Personal" },
            ]}
            clearable
          />
          {newEntityType && newEntityType !== "personal" && (
            <TextInput label="Entity ID" value={newEntityId} onChange={(e) => setNewEntityId(e.target.value)} placeholder="UUID of the linked entity" />
          )}
          <Textarea label="Notes" value={newNotes} onChange={(e) => setNewNotes(e.target.value)} placeholder="Optional notes about this loan" minRows={2} />
          <Group justify="flex-end" mt="md">
            <Button variant="default" onClick={() => setAddModalOpen(false)}>Cancel</Button>
            <Button onClick={handleAdd} loading={createMutation.isPending}>Add Loan</Button>
          </Group>
        </Stack>
      </Modal>
    </div>
  );
}