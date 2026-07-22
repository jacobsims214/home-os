"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";

// ─── Types ────────────────────────────────────────────────────

interface Note {
  id: string;
  entity_type: string;
  entity_id: string;
  title: string | null;
  body: string;
  author_id: string | null;
  author_name: string | null;
  created_at: string;
}

interface NotesResponse {
  data: Note[];
}

// ─── Inline SVG icons ────────────────────────────────────────

function NoteIcon() {
  return (
    <svg className="h-5 w-5 text-gray-400" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
      <path strokeLinecap="round" strokeLinejoin="round" d="M19.5 14.25v-2.625a3.375 3.375 0 00-3.375-3.375h-1.5A1.125 1.125 0 0113.5 7.125v-1.5a3.375 3.375 0 00-3.375-3.375H8.25m0 12.75h7.5m-7.5 3H12M10.5 2.25H5.625c-.621 0-1.125.504-1.125 1.125v17.25c0 .621.504 1.125 1.125 1.125h12.75c.621 0 1.125-.504 1.125-1.125V11.25a9 9 0 00-9-9z" />
    </svg>
  );
}

// ─── Component ────────────────────────────────────────────────

interface NotesSectionProps {
  entityType: string;
  entityId: string;
}

export default function NotesSection({ entityType, entityId }: NotesSectionProps) {
  const queryClient = useQueryClient();
  const [showForm, setShowForm] = useState(false);
  const [noteTitle, setNoteTitle] = useState("");
  const [noteBody, setNoteBody] = useState("");

  const notesQ = useQuery({
    queryKey: ["notes", entityType, entityId],
    queryFn: () =>
      apiFetch<NotesResponse>(
        `/api/v1/notes?entity_type=${entityType}&entity_id=${entityId}`
      ),
    enabled: !!entityId,
  });

  const notes = notesQ.data?.data ?? [];

  const createMutation = useMutation({
    mutationFn: (body: { entity_type: string; entity_id: string; title?: string; body: string }) =>
      apiFetch("/api/v1/notes", { method: "POST", body }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["notes", entityType, entityId] });
      setShowForm(false);
      setNoteTitle("");
      setNoteBody("");
    },
  });

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!noteBody.trim()) return;
    createMutation.mutate({
      entity_type: entityType,
      entity_id: entityId,
      title: noteTitle.trim() || undefined,
      body: noteBody.trim(),
    });
  };

  const fmtDate = (iso: string) => {
    try {
      return new Date(iso).toLocaleDateString("en-US", {
        month: "short",
        day: "numeric",
        year: "numeric",
      });
    } catch {
      return iso;
    }
  };

  return (
    <section className="mt-8">
      <div className="flex items-center justify-between mb-4">
        <h2 className="text-lg font-semibold text-gray-900">Notes</h2>
        {!showForm && (
          <button
            onClick={() => setShowForm(true)}
            className="inline-flex items-center gap-1 rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors"
          >
            <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
              <path strokeLinecap="round" strokeLinejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
            </svg>
            Add Note
          </button>
        )}
      </div>

      <div className="space-y-3">
        {/* Note list */}
        {notesQ.isLoading && (
          <div className="space-y-3">
            {[1, 2].map((i) => (
              <div key={i} className="h-20 animate-pulse rounded-lg bg-gray-100" />
            ))}
          </div>
        )}

        {!notesQ.isLoading && notes.length === 0 && !showForm && (
          <p className="text-sm text-gray-400 italic">No notes yet.</p>
        )}

        {notes.map((note) => (
          <div
            key={note.id}
            className="rounded-lg border border-gray-200 bg-white px-4 py-3"
          >
            <div className="flex items-center gap-2 mb-1">
              <span className="inline-flex items-center justify-center h-6 w-6 rounded-full bg-indigo-50 text-xs font-medium text-indigo-700">
                {note.author_name?.charAt(0)?.toUpperCase() ?? "?"}
              </span>
              <span className="text-sm font-medium text-gray-900">
                {note.author_name ?? "Unknown"}
              </span>
              <span className="text-xs text-gray-400">· {fmtDate(note.created_at)}</span>
            </div>
            {note.title && (
              <p className="text-sm font-semibold text-gray-800 mb-0.5">{note.title}</p>
            )}
            <p className="text-sm text-gray-600 whitespace-pre-wrap">{note.body}</p>
          </div>
        ))}

        {/* Add Note form */}
        {showForm && (
          <form
            onSubmit={handleSubmit}
            className="rounded-lg border border-indigo-200 bg-indigo-50/30 p-4 space-y-3"
          >
            <input
              type="text"
              value={noteTitle}
              onChange={(e) => setNoteTitle(e.target.value)}
              placeholder="Title (optional)"
              className="block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm placeholder:text-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
            />
            <textarea
              value={noteBody}
              onChange={(e) => setNoteBody(e.target.value)}
              placeholder="Write a note..."
              rows={3}
              required
              className="block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm placeholder:text-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
            />
            <div className="flex justify-end gap-2">
              <button
                type="button"
                onClick={() => { setShowForm(false); setNoteTitle(""); setNoteBody(""); }}
                disabled={createMutation.isPending}
                className="inline-flex items-center rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50 disabled:bg-gray-100 transition-colors"
              >
                Cancel
              </button>
              <button
                type="submit"
                disabled={!noteBody.trim() || createMutation.isPending}
                className="inline-flex items-center rounded-md bg-indigo-600 px-3 py-1.5 text-sm font-semibold text-white hover:bg-indigo-500 disabled:bg-indigo-400 transition-colors"
              >
                {createMutation.isPending ? "Saving..." : "Save Note"}
              </button>
            </div>
          </form>
        )}
      </div>
    </section>
  );
}