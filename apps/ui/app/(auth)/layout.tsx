import HomeMark from "@/components/shared/HomeMark";

/**
 * The signed-out frame. The brand row is the same mark the shell's sidebar and
 * drawer use (#5200): the approved mockup's house glyph on the accent tile,
 * here at 40 px because there is no 28 px brand row to sit in. The tile is
 * `--accent-fill` (#0b7285 in both schemes) with a white glyph, so it reads the
 * same whatever the OS theme is, on this page's white card background.
 */
export default function AuthLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <div className="flex min-h-screen items-center justify-center px-4 py-12 sm:px-6 lg:px-8">
      <div className="w-full max-w-md space-y-8">
        <div className="flex items-center justify-center gap-3">
          <HomeMark size={40} />
          <h2 className="text-3xl font-bold tracking-tight text-gray-900">
            Home OS
          </h2>
        </div>
        <div className="rounded-lg border border-gray-200 bg-white px-6 py-8 shadow-sm sm:px-10">
          {children}
        </div>
      </div>
    </div>
  );
}
