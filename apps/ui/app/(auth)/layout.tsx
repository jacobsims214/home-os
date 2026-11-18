import HomeMark from "@/components/shared/HomeMark";

/**
 * The signed-out frame (#4562): the approved settings/onboarding mockup
 * (artifact #103 v1, its "standalone screens: auth" block) at phone width.
 *
 * THE FRAME, NOT THE CARD, IS THE PAGE ON A PHONE. The mockup draws `.stand`
 * (full height, `--bg`) -> `.auth` (a centred column, 24 px + safe-area
 * padding) -> the brand row (12 px above, 24 px below) -> `.acard`
 * (`max-width: 420px`, nothing else). The white card with its border, 16 px
 * radius and 32 px padding is a >= 768 px treatment (`.auth .acard` inside the
 * media query), so a phone gets the full-bleed page the artifact shows instead
 * of a card inset in a second card. Each page under (auth) therefore renders
 * only its `.acard` content: no page may draw its own centred min-h-screen
 * wrapper again, which is what put a card inside a card at 390 px before this
 * ticket.
 *
 * The brand row is the mark the shell's sidebar uses (#5200), at the mockup's
 * auth size (36 px tile, 18 px wordmark). Colours are tokens only
 * (app/globals.css), so light and dark both work.
 *
 * Padding is written with `env(safe-area-inset-*)` rather than Tailwind's
 * `safe-bottom`: the top inset matters as much as the bottom on a phone held
 * in landscape, and the artifact pads both.
 */
export default function AuthLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <div className="flex min-h-[100dvh] flex-col bg-bg">
      {/* The artifact anchors the block to the TOP on a phone and centres it
          from 768 px up: `.auth` has no `justify-content` until the media query
          adds `center`. That is the phone behaviour people expect too -- the
          heading sits where the eye starts, not halfway down a long screen. */}
      <div className="flex flex-1 flex-col items-center justify-start px-4 py-[calc(24px+env(safe-area-inset-top,0px))] pb-[calc(24px+env(safe-area-inset-bottom,0px))] md:justify-center md:px-6 md:py-10">
        <div className="mb-6 flex items-center justify-center gap-2.5">
          <HomeMark size={36} />
          <span className="text-[18px] font-semibold leading-none tracking-[-0.01em] text-ink">
            Home OS
          </span>
        </div>
        <div className="w-full max-w-[420px] md:rounded-token-lg md:border md:border-line md:bg-surface md:p-8 md:shadow-token-sm">
          {children}
        </div>
        {/* The artifact's `.afoot`: it names the product and says the instance
            is self-hosted, which is the reassurance a family member signing in
            to someone's home server actually needs. The mockup writes "by the
            Sims family" because a mockup knows whose it is; a signed-out page
            does not, so the family name is the deliberate difference here. */}
        <p className="mt-4 text-center text-xs text-ink-3 md:mt-6">Home OS · self-hosted</p>
      </div>
    </div>
  );
}
